//go:build e2edataplane && e2edataplanevm

package dataplane

// The containers loop (issue #423, LOOP.md "Containers, pooled nodes and
// snapshot restore"): the e2e/dataplane cluster (real tw-mgmt, real
// tw-hostd processes on testcontrol + a local DERP, the fake Tailscale API)
// with a microVM backend on every host: Virtualization.framework on macOS,
// Firecracker + jailer (as root) on Linux. Many Box objects each own a
// container (ADR-032) and their own node (from the pre-joined pool,
// ADR-031); objects come in bursts, and in the restore scenario so do
// microVM-tier workloads (the warm pool and the base snapshot, ADR-030).
// The nemesis kills hostd, the VMMs and whole hosts mid-start, moves the
// parent, cuts a host off, stalls the Tailscale API and crashes a host in
// the middle of a burst. The checkers are K1–K4 (check_containers_test.go).
//
// make e2e-dataplane-vm (needs a Mac's virtualization or Linux + KVM as
// root; not in the credential-free nightly).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/oci"
	"github.com/rosskukulinski/tailworks/internal/registry"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const (
	ctrWorkload = "ctrs"
	ctrGrace    = 3 * time.Second // -container-stop-grace
	// ctrFenceBound: how long after a lease change (or the end of a fault on
	// that host) the old host may still run a container: FenceAfter (30 s)
	// plus a heartbeat (3 s), with slack for the stop itself.
	ctrFenceBound = 45 * time.Second
)

// ---- the fake Tailscale API, with a stall switch ----

// stallTS is the harness's fakeTS plus a mode the nemesis flips: "" (normal),
// "slow" (every call waits 4 s) or "error" (every call but the token
// answers 503). It records device deletes.
type stallTS struct {
	srv     *httptest.Server
	mode    atomic.Value // string
	mu      sync.Mutex
	keys    int
	deletes map[string]time.Time
}

func newStallTS(t *testing.T) *stallTS {
	s := &stallTS{deletes: map[string]time.Time{}}
	s.mode.Store("")
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": fakeToken, "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != fakeToken {
			w.WriteHeader(401)
			return
		}
		switch s.mode.Load().(string) {
		case "slow":
			time.Sleep(4 * time.Second)
		case "error":
			w.WriteHeader(503)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
			s.mu.Lock()
			s.keys++
			id := fmt.Sprintf("k%06d", s.keys)
			s.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "fake-authkey-" + id})
		case r.Method == "DELETE":
			if id, ok := strings.CutPrefix(r.URL.Path, "/api/v2/device/"); ok {
				s.mu.Lock()
				s.deletes[id] = time.Now()
				s.mu.Unlock()
			}
			w.WriteHeader(200)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
			w.Header().Set("ETag", `"p1"`)
			w.Write([]byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
			json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stallTS) deleted() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Time{}
	for k, v := range s.deletes {
		out[k] = v
	}
	return out
}

// ---- the history of this loop ----

// ctrOp is one client call to a Box object.
type ctrOp struct {
	Obj, Op    string // up | down | kill
	Sent, Recv time.Time
	OK         bool
	Status     int
	Boot, Rnd  string `json:",omitempty"`
	Started    bool   `json:",omitempty"`
	Epoch      int64  `json:",omitempty"`
	MagicDNS   string `json:",omitempty"`
	ObjectID   string `json:",omitempty"`
	MS         int64  `json:",omitempty"` // down: destroy() took
	Err        string `json:",omitempty"`
}

// ctrRep is a Box's report as the recorder received it: Node and StableID
// are the recorder's WhoIs of the sender (the object's own node).
type ctrRep struct {
	At                   time.Time
	Node, StableID       string
	Obj, Boot, Rnd       string
	Epoch                int64
	MagicDNS, ObjectID   string
	Started              bool
	ReportedHostnameSelf string `json:"Hostname"`
	Status               string // ctx.tailnet.self().status when the object read its name
}

// vmRep is a microVM workload's first random draws, as the recorder got them.
type vmRep struct {
	At             time.Time
	Node, StableID string // the recorder's WhoIs ("" for a client answer)
	WL, UUID, Rnd  string
	MR             float64
	RecErr         string `json:",omitempty"` // the workload's report failed
	Client         bool   `json:",omitempty"` // the /id answer, not the report
}

// hostObs is one poll of a host's status socket (only this loop's
// workloads). Down: the socket didn't answer (stopped, restarting).
type hostObs struct {
	At    time.Time
	Host  string
	Down  bool             `json:",omitempty"`
	Ctrs  []obsCtr         `json:",omitempty"`
	Objs  []obsObj         `json:",omitempty"`
	VMs   []obsVM          `json:",omitempty"`
	VMMs  []int            `json:",omitempty"` // VMM processes under this hostd
	Extra map[string]int64 `json:",omitempty"`
}

type obsCtr struct {
	Obj, State string
	Gen        int64
	PID        int
}

type obsObj struct {
	ID, Class, Name, Status, NodeID, MagicDNS string
}

type obsVM struct {
	Workload, Source string
	PID              int
}

// mgmtObj is one object as mgmt lists it.
type mgmtObj struct {
	ObjectID     string `json:"objectID"`
	Namespace    string `json:"namespace"`
	ObjectName   string `json:"objectName"`
	Workload     string `json:"workload"`
	Status       string `json:"status"`
	Host         string `json:"host"`
	StableNodeID string `json:"stableNodeID"`
	MagicDNS     string `json:"magicDNS"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type mgmtSnap struct {
	At   time.Time
	Objs []mgmtObj
}

type kHistory struct {
	h     *history // shared: faults, deployments, the jsonl file
	mu    sync.Mutex
	ops   []ctrOp
	reps  []ctrRep
	vms   []vmRep
	obs   []hostObs
	snaps []mgmtSnap
	last  map[string]string // host -> last obs written (dedup)
	vmmEv map[int]string    // every VMM pid ever seen -> host
}

func (k *kHistory) log(typ string, v any) {
	k.h.mu.Lock()
	k.h.log(typ, v)
	k.h.mu.Unlock()
}

func (k *kHistory) op(o ctrOp) {
	k.mu.Lock()
	k.ops = append(k.ops, o)
	k.mu.Unlock()
	k.log("ctr", o)
}

// ---- the workload ----

type ctrLoad struct {
	c      *cluster
	k      *kHistory
	tg     *target
	cli    *http.Client
	rnd    *rand.Rand
	rmu    sync.Mutex
	stop   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	live   []string        // Box objects in creation order
	cctx   context.Context // calls in flight end with it (canceled at stop)
	next   int
	burst  int
	maxObj int
}

func (l *ctrLoad) intn(n int) int {
	l.rmu.Lock()
	defer l.rmu.Unlock()
	return l.rnd.Intn(n)
}

func (l *ctrLoad) call(obj, op string) ctrOp {
	d := l.tg.get()
	l.mu.Lock()
	ctx := l.cctx
	l.mu.Unlock()
	o := ctrOp{Obj: obj, Op: op, Sent: time.Now()}
	rq, _ := http.NewRequestWithContext(ctx, "GET", "http://"+d.Endpoint.MagicDNS+"/c/"+obj+"/"+op, nil)
	resp, err := l.cli.Do(rq)
	if err != nil {
		o.Recv, o.Err = time.Now(), err.Error()
		l.k.op(o)
		return o
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	o.Recv, o.Status = time.Now(), resp.StatusCode
	var r struct {
		Boot, Rnd, MagicDNS, ObjectID, Error string
		Started                              bool
		Epoch, MS                            int64
	}
	json.Unmarshal(b, &r)
	o.OK = resp.StatusCode == 200
	o.Boot, o.Rnd, o.Started, o.Epoch, o.MagicDNS, o.ObjectID, o.MS = r.Boot, r.Rnd, r.Started, r.Epoch, r.MagicDNS, r.ObjectID, r.MS
	if o.OK && op == "up" && (o.Boot == "" || o.Epoch == 0) {
		// Not the object's answer (seen once right after a hostd restart).
		o.OK = false
		b = append([]byte("200 without a report: "), b...)
	}
	if !o.OK {
		o.Err = strings.TrimSpace(string(b))
		if len(o.Err) > 300 {
			o.Err = o.Err[:300]
		}
	}
	l.k.op(o)
	return o
}

// newBurst creates n new objects at once (each up starts its container):
// they take pool nodes and boot VMs concurrently. It returns when all
// answered.
func (l *ctrLoad) newBurst(n int) {
	var names []string
	l.mu.Lock()
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("o%03d", l.next))
		l.next++
	}
	l.live = append(l.live, names...)
	l.mu.Unlock()
	var wg sync.WaitGroup
	for _, nm := range names {
		wg.Add(1)
		go func() { defer wg.Done(); l.call(nm, "up") }()
	}
	wg.Wait()
}

// retire destroys and kills the oldest objects past maxObj.
func (l *ctrLoad) retire() {
	l.mu.Lock()
	var old []string
	for len(l.live) > l.maxObj {
		old = append(old, l.live[0])
		l.live = l.live[1:]
	}
	l.mu.Unlock()
	for _, o := range old {
		d := l.call(o, "down")
		l.kill(o, d.ObjectID)
	}
}

// kill ends an object as an operator does (`tw kill`: mgmt's DELETE), so
// the answer says whether mgmt took it.
func (l *ctrLoad) kill(obj, id string) {
	if id == "" {
		l.k.mu.Lock()
		for _, o := range l.k.ops {
			if o.Obj == obj && o.ObjectID != "" {
				id = o.ObjectID
			}
		}
		l.k.mu.Unlock()
	}
	o := ctrOp{Obj: obj, Op: "kill", Sent: time.Now(), ObjectID: id}
	if id == "" {
		o.Recv, o.Err = time.Now(), "no object ID seen"
		l.k.op(o)
		return
	}
	st, b, err := l.c.try("DELETE", "/v1/objects/"+id+"?reason=dataplane+loop+rolling+window", nil, nil)
	o.Recv, o.Status, o.OK = time.Now(), st, err == nil && st/100 == 2
	if !o.OK {
		o.Err = fmt.Sprintf("%d %s %v", st, strings.TrimSpace(string(b)), err)
	}
	l.k.op(o)
}

func (l *ctrLoad) every(d time.Duration, f func()) {
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				f()
			}
		}
	}()
}

func (l *ctrLoad) run() {
	// Touch a random live object every second (concurrent ones allowed).
	l.every(time.Second, func() {
		l.mu.Lock()
		if len(l.live) == 0 {
			l.mu.Unlock()
			return
		}
		o := l.live[l.intn(len(l.live))]
		l.mu.Unlock()
		go l.call(o, "up")
	})
	// A burst of new objects, then retire the oldest.
	l.every(25*time.Second, func() {
		l.newBurst(l.burst)
		l.retire()
	})
}

// ---- observers ----

func statusClient(sock string) *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// isVMM reports whether pid is a VMM process (tw-vz, firecracker, jailer).
func isVMM(pid int) bool {
	c := filepath.Base(comm(pid))
	return strings.Contains(c, "tw-vz") || strings.Contains(c, "firecracker") || strings.Contains(c, "jailer")
}

func vmmsUnder(pid int) []int {
	if pid == 0 {
		return nil
	}
	var out []int
	for _, k := range descendants(pid) {
		if isVMM(k) {
			out = append(out, k)
		}
	}
	sort.Ints(out)
	return out
}

// pollHost reads one host's status socket.
func (l *ctrLoad) pollHost(id string, vmws func() map[string]bool) hostObs {
	ob := hostObs{At: time.Now(), Host: id}
	l.c.mu.Lock()
	stopped := l.c.stopped[id]
	l.c.mu.Unlock()
	if stopped {
		ob.Down = true
		return ob
	}
	hc := statusClient(filepath.Join(l.c.work, id, "run", "hostd.sock"))
	resp, err := hc.Get("http://hostd/v1/status")
	if err != nil {
		ob.Down = true
		return ob
	}
	defer resp.Body.Close()
	var st struct {
		Objects []struct {
			ID, Workload, Class, Name, Status, NodeID, MagicDNS string
		}
		Workloads []struct {
			Workload string
			VM       *struct {
				PID    int
				Source string
			}
		}
		Containers []struct {
			Workload, Object, State string
			Gen                     int64
			PID                     int
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		ob.Down = true
		return ob
	}
	mine := vmws()
	for _, o := range st.Objects {
		if o.Workload == ctrWorkload || mine[o.Workload] {
			ob.Objs = append(ob.Objs, obsObj{o.ID, o.Class, o.Name, o.Status, o.NodeID, o.MagicDNS})
		}
	}
	for _, w := range st.Workloads {
		if w.VM != nil && mine[w.Workload] {
			ob.VMs = append(ob.VMs, obsVM{w.Workload, w.VM.Source, w.VM.PID})
		}
	}
	for _, ct := range st.Containers {
		if ct.Workload == ctrWorkload {
			ob.Ctrs = append(ob.Ctrs, obsCtr{ct.Object, ct.State, ct.Gen, ct.PID})
		}
	}
	ob.VMMs = vmmsUnder(l.c.pid(id))
	return ob
}

func (l *ctrLoad) observe(vmws func() map[string]bool) {
	l.every(time.Second, func() {
		var wg sync.WaitGroup
		obs := make([]hostObs, len(l.c.hosts))
		for i, id := range l.c.hosts {
			wg.Add(1)
			go func() { defer wg.Done(); obs[i] = l.pollHost(id, vmws) }()
		}
		wg.Wait()
		k := l.k
		for _, ob := range obs {
			key, _ := json.Marshal(struct {
				A, B, C, D any
				E          bool
			}{ob.Ctrs, ob.Objs, ob.VMs, ob.VMMs, ob.Down})
			k.mu.Lock()
			k.obs = append(k.obs, ob)
			for _, p := range ob.VMMs {
				k.vmmEv[p] = ob.Host
			}
			for _, ct := range ob.Ctrs {
				if ct.PID != 0 {
					k.vmmEv[ct.PID] = ob.Host
				}
			}
			changed := k.last[ob.Host] != string(key)
			k.last[ob.Host] = string(key)
			k.mu.Unlock()
			if changed {
				k.log("obs", ob)
			}
		}
	})
	// mgmt's view of this loop's objects, every 3 s.
	l.every(3*time.Second, func() {
		if objs, err := l.c.listObjects(); err == nil {
			s := mgmtSnap{At: time.Now(), Objs: objs}
			l.k.mu.Lock()
			l.k.snaps = append(l.k.snaps, s)
			l.k.mu.Unlock()
		}
	})
}

// listObjects pages through mgmt's objects of the tenant (every workload:
// Box objects, pool nodes, entry nodes).
func (c *cluster) listObjects() ([]mgmtObj, error) {
	var out []mgmtObj
	cursor := ""
	for i := 0; i < 50; i++ {
		var pg struct {
			Items      []mgmtObj `json:"items"`
			NextCursor string    `json:"nextCursor"`
		}
		path := "/v1/objects?tenant=" + c.tenantID + "&limit=500"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		st, b, err := c.try("GET", path, nil, &pg)
		if err != nil {
			return nil, err
		}
		if st != 200 {
			return nil, fmt.Errorf("%d %s", st, b)
		}
		out = append(out, pg.Items...)
		if pg.NextCursor == "" {
			return out, nil
		}
		cursor = pg.NextCursor
	}
	return out, nil
}

func serveCtrRecorder(n *nettest.Node, k *kHistory) error {
	ln, err := n.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		at := time.Now()
		node, sid := "", ""
		if who, err := n.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil {
			node, sid = who.Node.Name, string(who.Node.StableID)
		}
		switch r.URL.Path {
		case "/ctr":
			var rep ctrRep
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				rw.WriteHeader(400)
				return
			}
			rep.At, rep.Node, rep.StableID = at, node, sid
			k.mu.Lock()
			k.reps = append(k.reps, rep)
			k.mu.Unlock()
			k.log("ctrrep", rep)
			rw.WriteHeader(204)
		case "/vm":
			var rep vmRep
			if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
				rw.WriteHeader(400)
				return
			}
			rep.At, rep.Node, rep.StableID = at, node, sid
			k.mu.Lock()
			k.vms = append(k.vms, rep)
			k.mu.Unlock()
			k.log("vmrep", rep)
			rw.WriteHeader(204)
		default:
			rw.WriteHeader(404)
		}
	}))
	return nil
}

// ---- microVM-tier bursts (ADR-030) ----

type vmBurster struct {
	c      *cluster
	k      *kHistory
	cli    *http.Client
	bundle string
	n      int
	mu     sync.Mutex
	seq    int
	names  map[string]bool // every vm workload ever deployed
}

func (v *vmBurster) all() map[string]bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]bool{}
	for k := range v.names {
		out[k] = true
	}
	return out
}

// burst deploys n microVM workloads onto host at once, asks each for its
// first random draws, then undeploys them. It returns a summary.
func (v *vmBurster) burst(host string) string {
	type res struct {
		wl, err string
		src     string
	}
	out := make([]res, v.n)
	var wg sync.WaitGroup
	for i := 0; i < v.n; i++ {
		v.mu.Lock()
		v.seq++
		wl := fmt.Sprintf("vmw%03d", v.seq)
		v.names[wl] = true
		v.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].wl = wl
			var d deployment
			st, b, err := v.c.try("POST", "/v1/deployments", map[string]any{"tenantID": v.c.tenantID, "workload": wl, "bundle": v.bundle, "host": host}, &d)
			if err != nil || st != 202 {
				out[i].err = fmt.Sprintf("deploy %d %s %v", st, strings.TrimSpace(string(b)), err)
				return
			}
			d, err = v.c.waitActive(d.DeploymentID, 120*time.Second)
			if err != nil {
				out[i].err = err.Error()
			} else {
				var id vmRep
				for try := 0; try < 30; try++ {
					if _, err = getJSON(v.cli, "http://"+d.Endpoint.MagicDNS+"/id", &id); err == nil {
						break
					}
					time.Sleep(time.Second)
				}
				if err != nil {
					out[i].err = "id: " + err.Error()
				} else {
					id.At, id.Client = time.Now(), true
					v.k.mu.Lock()
					v.k.vms = append(v.k.vms, id)
					v.k.mu.Unlock()
					v.k.log("vmid", id)
				}
			}
			if d.DeploymentID != "" {
				v.c.try("DELETE", "/v1/deployments/"+d.DeploymentID, nil, nil)
			}
		}()
	}
	wg.Wait()
	var errs []string
	for _, r := range out {
		if r.err != "" {
			errs = append(errs, r.wl+": "+r.err)
		}
	}
	return fmt.Sprintf("%d microVM workloads on %s, %d failed %v", v.n, host, len(errs), errs)
}

// ---- the nemesis additions ----

// kfault is a fault of this loop; it may use the loop's workload.
type kfault struct {
	name string
	run  func(n *nemesis, kl *kLoop, host string) string
}

type kLoop struct {
	load *ctrLoad
	ts   *stallTS
	vmb  *vmBurster
	vmm  map[int]bool // VMMs that outlived a crashed hostd (K4)
	mu   sync.Mutex
}

// Sorted by name. Faults of the shared nemesis (nemesis_test.go) are used
// by name too.
var kfaults = map[string]kfault{
	// The Tailscale API answers 503 (or 4 s late) while a burst of new
	// objects wants nodes and the pools refill.
	"api-stall": {"api-stall", func(n *nemesis, kl *kLoop, h string) string {
		mode := []string{"error", "slow"}[n.rnd.Intn(2)]
		d := n.between(5*time.Second, 20*time.Second)
		kl.ts.mode.Store(mode)
		go kl.load.newBurst(kl.load.burst)
		time.Sleep(d)
		kl.ts.mode.Store("")
		return fmt.Sprintf("%s for %v", mode, d)
	}},
	// A burst of new objects, then SIGKILL the whole host (hostd, workerd,
	// VMMs) mid-adoption: pool nodes being renamed, containers booting.
	"burst-crash": {"burst-crash", func(n *nemesis, kl *kLoop, h string) string {
		after := n.between(0, 1500*time.Millisecond)
		down := n.between(time.Second, 3*time.Second)
		go kl.load.newBurst(kl.load.burst)
		time.Sleep(after)
		n.c.stop(h, true)
		time.Sleep(down)
		n.c.startHost(h)
		n.c.waitHostsLive(h)
		return fmt.Sprintf("killed %v into the burst, down %v", after, down)
	}},
	// SIGKILL hostd alone; its workerds and VMMs are left as a crash leaves
	// them. 15 s after the restart, none of the old VMMs may still run (K4).
	"crash-hostd-vmm": {"crash-hostd-vmm", func(n *nemesis, kl *kLoop, h string) string {
		pid := n.c.pid(h)
		old := vmmsUnder(pid)
		var wds []int
		for _, k := range descendants(pid) {
			if strings.Contains(comm(k), "workerd") {
				wds = append(wds, k)
			}
		}
		n.c.crashOnly(h)
		time.Sleep(n.between(time.Second, 3*time.Second))
		n.c.startHost(h)
		n.c.waitHostsLive(h)
		time.Sleep(15 * time.Second)
		var alive []int
		for _, p := range old {
			if syscall.Kill(p, 0) == nil && isVMM(p) {
				alive = append(alive, p)
				n.c.orphan(h+"/vmm", p)
				kl.mu.Lock()
				kl.vmm[p] = true
				kl.mu.Unlock()
			}
		}
		for _, p := range wds {
			if syscall.Kill(p, 0) == nil {
				n.c.orphan(h, p)
			}
		}
		return fmt.Sprintf("old VMMs %v; still alive 15 s after the restart: %v", old, alive)
	}},
	// SIGKILL one VMM (a container's or a microVM workload's) under the
	// host's hostd; the object's next up starts a new container.
	"kill-vmm": {"kill-vmm", func(n *nemesis, kl *kLoop, h string) string {
		vs := vmmsUnder(n.c.pid(h))
		if len(vs) == 0 {
			return "no VMM running"
		}
		p := vs[n.rnd.Intn(len(vs))]
		name := filepath.Base(comm(p))
		n.c.record(h+"/vmm(kill)", p)
		syscall.Kill(p, syscall.SIGKILL)
		time.Sleep(2 * time.Second)
		return fmt.Sprintf("killed VMM %d (%s) of %d", p, name, len(vs))
	}},
	// A blue/green redeploy on the same host: a container survives it
	// (ADR-032: durable_object instances never roll), checked by K1.
	"redeploy": {"redeploy", func(n *nemesis, kl *kLoop, h string) string {
		d, st, b, err := n.c.deploy(n.bundle, h, true, false)
		if err != nil || st != 202 {
			return fmt.Sprintf("FAILED: %d %s %v", st, b, err)
		}
		n.tg.mu.Lock()
		n.tg.depID = d.DeploymentID
		n.tg.mu.Unlock()
		d, err = n.c.waitActive(d.DeploymentID, 120*time.Second)
		if err != nil {
			return "FAILED: " + err.Error()
		}
		n.tg.set(d)
		time.Sleep(3 * time.Second)
		return fmt.Sprintf("on %s epoch %d", d.Host, d.Epoch)
	}},
	// A burst of microVM-tier workloads (warm pool + snapshot restore on
	// Firecracker; cold boots on vz) on the holder's host.
	"vm-burst": {"vm-burst", func(n *nemesis, kl *kLoop, h string) string {
		if kl.vmb.n == 0 {
			return "TW_DPK_VMS=0"
		}
		return kl.vmb.burst(h)
	}},
}

var kscenarios = map[string][]string{
	"containers": {"crash-hostd-vmm", "cut-takeover", "kill-host", "kill-vmm", "move", "pause-takeover", "redeploy"},
	"mixed":      {"api-stall", "burst-crash", "crash-hostd-vmm", "cut-short", "cut-takeover", "kill-host", "kill-vmm", "move", "pause-takeover", "redeploy", "restart-mgmt", "vm-burst"},
	"pool":       {"api-stall", "burst-crash", "kill-host", "move", "restart-mgmt"},
	"restore":    {"burst-crash", "kill-host", "kill-vmm", "vm-burst"},
}

func (n *nemesis) runK(kl *kLoop, names []string, until time.Time, logf func(string, ...any)) {
	for time.Now().Before(until) {
		time.Sleep(n.between(3*time.Second, 8*time.Second))
		if !time.Now().Before(until) {
			return
		}
		name := names[n.rnd.Intn(len(names))]
		host := n.tg.get().Host
		start := time.Now()
		var detail string
		if f, ok := kfaults[name]; ok {
			detail = f.run(n, kl, host)
		} else {
			detail = faults[name].run(n, host)
		}
		ev := faultEv{Kind: name, Host: host, Start: start, End: time.Now(), Detail: detail}
		n.h.fault(ev)
		logf("nemesis: %s on %s (%.1fs): %s", name, host, ev.End.Sub(start).Seconds(), detail)
	}
}

// ---- the test ----

// kvmHostArgs are the tw-hostd flags of the microVM backend on this OS.
func kvmHostArgs(t *testing.T, images string) []string {
	args := []string{"-container-image-dir", images, "-container-stop-grace", ctrGrace.String(),
		"-pool-nodes", envOr("TW_DPK_POOL_NODES", "2"), "-vm-mem-mib", envOr("TW_DPK_VM_MEM_MIB", "256"), "-vm-vcpus", "1"}
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s must be set (make e2e-dataplane-vm)", k)
		}
		return v
	}
	switch runtime.GOOS {
	case "darwin":
		args = append(args, "-vz-vmm", need("TW_VZ_VMM"), "-vm-kernel", need("TW_VM_KERNEL"), "-vm-rootfs", need("TW_VM_ROOTFS"))
	case "linux":
		if os.Geteuid() != 0 {
			t.Skip("Firecracker without tw-vmhelper needs root (make e2e-dataplane-vm runs the test binary with sudo)")
		}
		args = append(args, "-vm-kernel", need("TW_VM_KERNEL"), "-vm-rootfs", need("TW_VM_ROOTFS"),
			"-vm-container-kernel", need("TW_VM_CONTAINER_KERNEL"), "-vm-helper", "/nonexistent",
			"-firecracker", envOr("TW_VM_FIRECRACKER", "/usr/local/bin/firecracker"), "-jailer", envOr("TW_VM_JAILER", "/usr/local/bin/jailer"),
			"-vm-pool", envOr("TW_DPK_VM_POOL", "1"), "-vm-hugepages", envOr("TW_VM_HUGEPAGES", "auto"))
	default:
		t.Skipf("no microVM backend on %s", runtime.GOOS)
	}
	return args
}

// TestContainers runs one scenario of the containers loop (TW_DPK_SCENARIO:
// containers | pool | restore | mixed, default mixed) for TW_DPK_DURATION
// (default 4m) with seed TW_DPK_SEED (default today's date), heals, waits
// for quiescence and checks K1–K4. Artifacts:
// e2e/dataplane/results/<stamp>-k-<scenario>-<seed>/.
func TestContainers(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	images := os.Getenv("TW_CTR_IMAGES")
	if images == "" {
		t.Skip("TW_CTR_IMAGES must be set: a dir with images.json (make e2e-dataplane-vm)")
	}
	hostArgs := kvmHostArgs(t, images)
	scenario := envOr("TW_DPK_SCENARIO", "mixed")
	names, ok := kscenarios[scenario]
	if !ok {
		t.Fatalf("TW_DPK_SCENARIO %q: one of containers, pool, restore, mixed", scenario)
	}
	seed, err := strconv.ParseInt(envOr("TW_DPK_SEED", time.Now().UTC().Format("20060102")), 10, 64)
	if err != nil {
		t.Fatalf("TW_DPK_SEED: %v", err)
	}
	dur, err := time.ParseDuration(envOr("TW_DPK_DURATION", "4m"))
	if err != nil {
		t.Fatalf("TW_DPK_DURATION: %v", err)
	}
	if fs := os.Getenv("TW_DPK_FAULTS"); fs != "" {
		names = strings.Split(fs, ",")
		for _, n := range names {
			if _, ok := kfaults[n]; !ok {
				if _, ok := faults[n]; !ok {
					t.Fatalf("TW_DPK_FAULTS: unknown fault %q", n)
				}
			}
		}
	}
	nhosts, _ := strconv.Atoi(envOr("TW_DPK_HOSTS", "3"))
	nvms, _ := strconv.Atoi(envOr("TW_DPK_VMS", "3"))
	burst, _ := strconv.Atoi(envOr("TW_DPK_BURST", "3"))
	maxObj, _ := strconv.Atoi(envOr("TW_DPK_MAX_OBJECTS", "6"))
	t.Logf("scenario %s, seed %d, duration %v, faults %v, %d hosts, bursts of %d objects (≤ %d live), %d microVM workloads per vm-burst",
		scenario, seed, dur, names, nhosts, burst, maxObj, nvms)

	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-k-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-dpk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	pids, err := os.OpenFile(filepath.Join(rdir, "pids"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pids.Close() })
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	ts := newStallTS(t)

	hosts := []string{"h1", "h2", "h3"}[:max(2, min(3, nhosts))]
	c := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: ts.srv.URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: hosts,
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{},
		workload: ctrWorkload, hostArgs: func(string) []string { return hostArgs }}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}

	nodes := map[string]*nettest.Node{}
	var nmu sync.Mutex
	var wg sync.WaitGroup
	for _, hn := range []string{"dpk-cli", "dpk-rec"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, hn, filepath.Join(work, hn))
			if err != nil {
				t.Error(err)
				return
			}
			t.Cleanup(func() { n.Close() })
			nmu.Lock()
			nodes[hn] = n
			nmu.Unlock()
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	h, err := newHistory(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	k := &kHistory{h: h, last: map[string]string{}, vmmEv: map[int]string{}}
	if err := serveCtrRecorder(nodes["dpk-rec"], k); err != nil {
		t.Fatal(err)
	}
	recURL := "http://" + nodes["dpk-rec"].IP4

	keyFile := filepath.Join(work, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	c.startMgmt(true)
	var tn struct{ TenantID string }
	c.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	c.tenantID = tn.TenantID
	for _, id := range c.hosts {
		c.proxies[id] = startProxy(t, filepath.Join(work, id+"-mgmt.sock"), c.sock)
		c.startHost(id)
	}
	c.waitHostsLive(c.hosts...)

	build := func(dir string, rewrite func(map[string][]byte)) string {
		app, _ := filepath.Abs(filepath.Join("testdata", "containers", dir))
		files, err := hostdtest.Build(app, map[string]string{"REC": recURL})
		if err != nil {
			t.Fatal(err)
		}
		if rewrite != nil {
			rewrite(files)
		}
		return base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	}
	img := envOr("TW_DPK_IMAGE", "tw.local/debian-trixie-python:1")
	pushPublicImage(t, c, images, img)
	bundle := build("ctr", func(f map[string][]byte) {
		f["tailworks.jsonc"] = []byte(strings.ReplaceAll(string(f["tailworks.jsonc"]), "tw.local/debian-trixie-python:1", img))
	})
	vmBundle := build("vm", nil)

	tg := &target{seen: map[string]string{}}
	d, st, b, err := c.deploy(bundle, c.hosts[0], false, false)
	if err != nil || st != 202 {
		c.dumpLogs()
		t.Fatalf("deploy: %d %s %v", st, b, err)
	}
	tg.depID = d.DeploymentID
	if d, err = c.waitActive(d.DeploymentID, 120*time.Second); err != nil {
		c.dumpLogs()
		t.Fatal(err)
	}
	tg.set(d)
	t.Logf("%s on %s at epoch %d (%s)", ctrWorkload, d.Host, d.Epoch, d.Endpoint.MagicDNS)

	cli := nodes["dpk-cli"].HTTPClient(90 * time.Second)
	cctx, cancelCalls := context.WithCancel(context.Background())
	load := &ctrLoad{c: c, k: k, tg: tg, cli: cli, rnd: rand.New(rand.NewSource(seed + 1)), stop: make(chan struct{}), burst: burst, maxObj: maxObj, cctx: cctx}
	vmb := &vmBurster{c: c, k: k, cli: nodes["dpk-cli"].HTTPClient(30 * time.Second), bundle: vmBundle, n: nvms, names: map[string]bool{}}
	kl := &kLoop{load: load, ts: ts, vmb: vmb, vmm: map[int]bool{}}
	// The shared watcher records lease moves (depEv) into the history.
	w := &workload{c: c, h: h, tg: tg, stop: load.stop}
	w.watch()
	load.observe(vmb.all)

	// The first object: its container converts the image and boots (slow
	// the first time on each host); then the clock starts.
	first := time.Now()
	for i := 0; ; i++ {
		if o := load.call("o000", "up"); o.OK {
			break
		} else if i > 20 {
			c.dumpLogs()
			t.Fatalf("the first container never came up: %s", o.Err)
		}
		time.Sleep(3 * time.Second)
	}
	load.mu.Lock()
	load.live, load.next = []string{"o000"}, 1
	load.mu.Unlock()
	t.Logf("first container up in %.1fs", time.Since(first).Seconds())
	load.newBurst(burst)
	load.run()

	time.Sleep(10 * time.Second)
	n := &nemesis{c: c, h: h, tg: tg, rnd: rand.New(rand.NewSource(seed)), bundle: bundle}
	n.runK(kl, names, time.Now().Add(dur), t.Logf)

	// Heal.
	ts.mode.Store("")
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	t.Logf("healed; the clients run 15 s more")
	time.Sleep(15 * time.Second)
	close(load.stop)
	cancelCalls()
	load.wg.Wait()
	load.mu.Lock()
	load.cctx = context.Background()
	load.mu.Unlock()

	// Convergence: the deployment is active and every live object answers.
	conv := result{ID: "K-conv", Pass: true}
	cur := tg.get()
	if dd, err := c.waitActive(cur.DeploymentID, 60*time.Second); err != nil {
		conv.fail("deployment not active after healing: %v", err)
	} else {
		tg.set(dd)
	}
	load.mu.Lock()
	live := append([]string(nil), load.live...)
	load.mu.Unlock()
	finalUp := map[string]ctrOp{}
	for _, o := range live {
		by := time.Now().Add(90 * time.Second)
		for {
			r := load.call(o, "up")
			if r.OK {
				finalUp[o] = r
				break
			}
			if time.Now().After(by) {
				conv.fail("object %s not up 90 s after healing: %s", o, r.Err)
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	conv.Summary = fmt.Sprintf("%d live objects up %.1fs after healing", len(finalUp), time.Since(healed).Seconds())
	t.Logf("%s; settling", conv.Summary)

	// Quiescence: let fences, reapers and pool refills settle, then look.
	time.Sleep(time.Duration(envInt("TW_DPK_SETTLE_S", 45)) * time.Second)
	final := map[string]hostObs{}
	for _, id := range c.hosts {
		final[id] = load.pollHost(id, vmb.all)
	}
	finalObjs, ferr := c.listObjects()
	t.Logf("quiescent; keyscanning")
	scans := keyscans(c, final)

	k.mu.Lock()
	h.mu.Lock()
	in := kInput{ops: k.ops, reps: k.reps, vms: k.vms, obs: k.obs, snaps: k.snaps, faults: h.faults, deps: h.deps,
		final: final, finalObjs: finalObjs, finalErr: ferr, finalUp: finalUp, vmmEver: k.vmmEv, crashSurvivors: kl.vmm,
		deletes: ts.deleted(), scans: scans, hosts: c.hosts, leftovers: vmLeftovers(c)}
	res := []result{checkK1(in), checkK2(in), checkK3(in), checkK4(in), conv}
	writeJSON(rdir, "faults.json", h.faults)
	writeJSON(rdir, "final.json", map[string]any{"hosts": final, "objects": finalObjs})
	h.mu.Unlock()
	k.mu.Unlock()
	writeJSON(rdir, "report.json", map[string]any{"scenario": scenario, "seed": seed, "duration": dur.String(), "results": res})
	failed := false
	for _, r := range res {
		mark := "✓"
		if !r.Pass {
			mark, failed = "✗", true
		}
		t.Logf("%s %s: %s", mark, r.ID, r.Summary)
		for _, e := range r.Evidence {
			t.Logf("    %s", e)
		}
		for _, e := range r.Notes {
			t.Logf("    (note) %s", e)
		}
	}
	if failed {
		t.Errorf("invariants violated; seed %d, scenario %s; artifacts in %s", seed, scenario, rdir)
	}
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// keyscans runs the strict no-keys scan (ADR-032 D5: the parent's DO
// volume key, DEK and MAC key never enter the container) on running
// containers at quiescence, through the host's status socket: at most
// TW_DPK_KEYSCANS (1), as each scan reads the guest's disks whole.
func keyscans(c *cluster, final map[string]hostObs) map[string]string {
	out := map[string]string{}
	left := envInt("TW_DPK_KEYSCANS", 1)
	for _, id := range c.hosts {
		for _, ct := range final[id].Ctrs {
			if ct.State != "running" || left <= 0 {
				continue
			}
			left--
			hc := statusClient(filepath.Join(c.work, id, "run", "hostd.sock"))
			hc.Timeout = 5 * time.Minute
			resp, err := hc.Post("http://hostd/v1/vm/check?keyscan=1&tenant="+c.tenantID+"&workload="+ctrWorkload+"&container=Box/"+ct.Obj, "", nil)
			if err != nil {
				out[id+"/"+ct.Obj] = "error: " + err.Error()
				continue
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			out[id+"/"+ct.Obj] = fmt.Sprintf("%d %s", resp.StatusCode, b)
		}
	}
	return out
}

// pushPublicImage pushes ref from the images dir into mgmt's registry as a
// public base, as the operator's `tw image push` does: the deploy pins it
// by digest and each host pulls it from mgmt (ADR-032 D3).
func pushPublicImage(t *testing.T, c *cluster, images, ref string) {
	t.Helper()
	var idx map[string]string
	b, err := os.ReadFile(filepath.Join(images, "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &idx); err != nil || idx[ref] == "" {
		t.Fatalf("%s: no %s in images.json (%v)", images, ref, err)
	}
	mc, err := mgmtapi.NewClient("unix://" + c.sock)
	if err != nil {
		t.Fatal(err)
	}
	mc.TokenFile = filepath.Join(c.work, "token")
	r, err := oci.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	l, err := oci.OpenLayout(filepath.Join(images, idx[ref]))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := oci.Copy(context.Background(), l, "", l.Top.Digest, mc.OCI(), registry.PublicNamespace+"/"+r.Repository, nil, r.Tag); err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
}

// vmLeftovers lists, on Linux, the in-process VM helpers' network
// namespaces (twp<hostd pid>-…) of hostds this run started that are no
// longer running: a dead hostd's netns left behind (#587). Other netns on
// the machine are not this run's and are ignored.
func vmLeftovers(c *cluster) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	started := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(c.dir, "pids")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) == 3 && slices.Contains(c.hosts, f[1]) {
				started[f[2]] = true
			}
		}
	}
	running := map[string]bool{}
	for _, id := range c.hosts {
		if p := c.pid(id); p != 0 {
			running[strconv.Itoa(p)] = true
		}
	}
	var out []string
	ents, _ := os.ReadDir("/run/netns")
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), "twp")
		if !ok {
			continue
		}
		pid, _, _ := strings.Cut(rest, "-")
		if started[pid] && !running[pid] {
			out = append(out, "netns "+e.Name())
		}
	}
	return out
}
