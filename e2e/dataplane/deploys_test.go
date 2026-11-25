//go:build e2edataplane

package dataplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// The deploys loop (issue #421, LOOP.md "Deploys"): concurrent deploys,
// rollbacks, preview deploys, promotions and secret puts and deletes, from
// several clients through different mgmt replicas (TW_DP_MGMT, default
// pg3 here), under host, mgmt and Postgres faults. The workload
// (testdata/deploys) answers every request with the epoch, the bundle
// (its BUILD var) and digests of the secret values that serve it. The
// checkers (check_deploys_test.go) are V1–V6.

const (
	dpdWL     = "dpd"
	moveSlack = 5 * time.Second // a live move: the old host stops at its next heartbeat (3 s) or push
)

// secretNames are the workload's secrets. SEC_A and SEC_B each have one
// writer; SEC_C has two, through different replicas. SEC_P is put on a
// preview's own name, which no deployment is ever released (secrets are
// the workload's: ADR-048).
var secretNames = []string{"SEC_A", "SEC_B", "SEC_C"}

var previewAliases = []string{"pa", "pb"}

func previewWL(alias string) string { return dpdWL + "--preview-" + alias }

type dBundle struct {
	label, b64, sha string
	files           map[string][]byte // the base bundles (b1…b4) only
}

// depView is a deployment as deploys, rollbacks and promotions answer it.
type depView struct {
	DeploymentID, Workload, Host, Status, Error string
	Epoch                                       int64
	Endpoint                                    struct{ MagicDNS, Status string }
}

// depOp is one deploy, rollback, preview deploy or promotion.
type depOp struct {
	Client  string
	Kind    string // deploy | rollback | preview | promote
	Replica int
	Build   string `json:",omitempty"` // the bundle deployed (deploy, preview)
	Target  string `json:",omitempty"` // rollback: the deployment named; promote: the alias
	Alias   string `json:",omitempty"`
	Host    string `json:",omitempty"` // pinned
	Replace bool   `json:",omitempty"`
	Force   bool   `json:",omitempty"`

	Sent, Recv   time.Time
	Status       int
	Code         string `json:",omitempty"`
	Err          string `json:",omitempty"`
	DeploymentID string `json:",omitempty"`
	Workload     string `json:",omitempty"`
	Epoch        int64  `json:",omitempty"`
	RHost        string `json:",omitempty"`
	RollbackOf   string `json:",omitempty"`
}

// outcome is ok (2xx), failed (a definite refusal) or unknown (a transport
// error or a 5xx: the write may or may not have happened).
func outcome(st int) string {
	switch {
	case st/100 == 2:
		return "ok"
	case st == 0 || st >= 500:
		return "unknown"
	}
	return "failed"
}

// secOp is one secret put or delete. Values are never recorded: Digest is
// what the workload reports for it.
type secOp struct {
	Client   string
	Workload string
	Name     string
	Kind     string // put | delete
	Ver      int    `json:",omitempty"`
	Digest   string // "-" for a delete
	Replica  int
	Sent     time.Time
	Recv     time.Time
	Status   int
	Err      string `json:",omitempty"`
}

// infoObs is one GET /info answer (or failure) from an endpoint.
type infoObs struct {
	Workload, DNS, Host string
	Sent, Recv          time.Time
	OK                  bool
	Epoch               int64
	Inst                string // the Durable Object copy that answered
	Build, WBuild       string
	Sec, WSec           map[string]string
	Err                 string `json:",omitempty"`
}

// depRec is one deployment history record (GET …/deployments).
type depRec struct {
	DeploymentID, Workload, Host, Status, HostStatus, Error string
	Epoch, CreatedAt, ActiveAt                              int64
	BundleSHA256, RollbackOf, DeployedBy                    string
	BundleRetained                                          bool
}

// recEv is a change the watcher saw in the history.
type recEv struct {
	At     time.Time
	Kind   string // new | changed | vanished | meta
	Rec    depRec
	Was    *depRec `json:",omitempty"`
	Detail string  `json:",omitempty"`
}

type auditRec struct {
	Ts     time.Time
	Kind   string
	Code   string
	Detail map[string]any
}

// dhist is the deploys loop's part of the history; it writes to the same
// history.jsonl (and under the same lock) as the nemesis's faults.
type dhist struct {
	h     *history
	ops   []depOp
	secs  []secOp
	obs   []infoObs
	recs  []recEv
	audit []auditRec
}

func (d *dhist) op(o depOp) {
	d.h.mu.Lock()
	d.ops = append(d.ops, o)
	d.h.log("depop", o)
	d.h.mu.Unlock()
}

func (d *dhist) sec(o secOp) {
	d.h.mu.Lock()
	d.secs = append(d.secs, o)
	d.h.log("secop", o)
	d.h.mu.Unlock()
}

func (d *dhist) ob(o infoObs) {
	d.h.mu.Lock()
	d.obs = append(d.obs, o)
	d.h.log("info", o)
	d.h.mu.Unlock()
}

func (d *dhist) rec(e recEv) {
	d.h.mu.Lock()
	d.recs = append(d.recs, e)
	d.h.log("deprec", e)
	d.h.mu.Unlock()
}

type endpoint struct{ workload, host string }

type secretVal struct {
	name  string
	ver   int
	value string // in memory only (the log scan looks for it)
}

// deploysRun is the loop's state.
type deploysRun struct {
	c       *cluster
	dh      *dhist
	tg      *target // dpd, as the watcher last saw it (the nemesis targets its host)
	cli     *http.Client
	bundles []dBundle
	runID   string
	stop    chan struct{}
	wg      sync.WaitGroup

	mu      sync.Mutex
	labelOf map[string]string    // bundle SHA-256 → label
	vals    map[string]secretVal // digest → value
	nextVer map[string]int       // workload/name → next version
	eps     map[string]endpoint  // MagicDNS → workload, host
	recs    map[string]depRec    // deployment ID → latest record seen
	rr      int
	seq     int // bundles made
}

// bundle makes a new bundle from base (b1…b4, or a label made from one):
// its BUILD var is unique ("b2.17"), so every deploy has its own SHA-256
// and every answer names the deploy (or the rollback target) it came from.
func (d *deploysRun) bundle(base string) dBundle {
	base, _, _ = strings.Cut(base, ".")
	var src dBundle
	for _, b := range d.bundles {
		if b.label == base {
			src = b
		}
	}
	if src.files == nil {
		panic("no bundle " + base)
	}
	d.mu.Lock()
	d.seq++
	label := fmt.Sprintf("%s.%d", base, d.seq)
	d.mu.Unlock()
	files := map[string][]byte{}
	for k, v := range src.files {
		files[k] = v
	}
	var w map[string]any
	if err := json.Unmarshal(files["wrangler.jsonc"], &w); err != nil {
		panic(err)
	}
	w["vars"].(map[string]any)["BUILD"] = label
	files["wrangler.jsonc"], _ = json.MarshalIndent(w, "", "  ")
	tar := hostdtest.MustTar(files)
	sum := sha256.Sum256(tar)
	b := dBundle{label: label, b64: base64.StdEncoding.EncodeToString(tar), sha: hex.EncodeToString(sum[:])}
	d.mu.Lock()
	d.labelOf[b.sha] = label
	d.mu.Unlock()
	d.dh.h.mu.Lock()
	d.dh.h.log("bundle", map[string]string{"label": label, "sha": b.sha})
	d.dh.h.mu.Unlock()
	return b
}

// replica is the next replica, round-robin from the client's own.
func (d *deploysRun) replica() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rr++
	return d.rr % d.c.replicas()
}

func (d *deploysRun) noteEndpoint(v depView) {
	if v.Endpoint.MagicDNS == "" || v.Workload == "" {
		return
	}
	// The host is the node name's prefix (-hostname-prefix <host>-): a
	// deployment's view can still name the previous host's node for a
	// moment after a move.
	host, _, _ := strings.Cut(v.Endpoint.MagicDNS, "-")
	d.mu.Lock()
	d.eps[v.Endpoint.MagicDNS] = endpoint{v.Workload, host}
	d.mu.Unlock()
}

func errCode(b []byte) string {
	var e struct{ Code string }
	json.Unmarshal(b, &e)
	return e.Code
}

func bodyText(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// deploy posts a deployment of dpd (or, with alias, a preview of it)
// through replica rep and records the op.
func (d *deploysRun) deploy(client string, rep int, label, alias, host string, replace, force bool) depOp {
	bn := d.bundle(label)
	o := depOp{Client: client, Kind: "deploy", Replica: rep, Build: bn.label, Alias: alias, Host: host, Replace: replace, Force: force}
	body := map[string]any{"tenantID": d.c.tenantID, "workload": dpdWL, "bundle": bn.b64,
		"host": host, "replace": replace, "forceTakeover": force}
	if alias != "" {
		o.Kind = "preview"
		body["preview"], body["previewAlias"] = true, alias
	}
	var v depView
	o.Sent = time.Now()
	st, b, err := d.c.tryOn(rep, "POST", "/v1/deployments", body, &v)
	o.Recv, o.Status = time.Now(), st
	d.finish(&o, st, b, err, v)
	return o
}

func (d *deploysRun) finish(o *depOp, st int, b []byte, err error, v depView) {
	if err != nil {
		o.Err = err.Error()
	} else if st/100 != 2 {
		o.Code, o.Err = errCode(b), bodyText(b)
	} else {
		o.DeploymentID, o.Workload, o.Epoch, o.RHost = v.DeploymentID, v.Workload, v.Epoch, v.Host
		if o.Workload == "" {
			o.Workload = dpdWL
			if o.Alias != "" {
				o.Workload = previewWL(o.Alias)
			}
		}
		// Endpoints are learned from GET /v1/deployments/{id} once ready
		// (poll), which names the node as the host serves it.
	}
	d.dh.op(*o)
}

// rollback rolls dpd back to the deployment named, through replica rep.
func (d *deploysRun) rollback(client string, rep int, target string, host string, replace, force bool) depOp {
	o := depOp{Client: client, Kind: "rollback", Replica: rep, Target: target, Host: host, Replace: replace, Force: force}
	var res struct {
		RollbackOf string
		Deployment depView
	}
	o.Sent = time.Now()
	st, b, err := d.c.tryOn(rep, "POST", "/v1/tenants/"+d.c.tenantID+"/workloads/"+dpdWL+"/rollbacks",
		map[string]any{"deploymentID": target, "host": host, "replace": replace, "forceTakeover": force}, &res)
	o.Recv, o.Status, o.RollbackOf = time.Now(), st, res.RollbackOf
	d.mu.Lock()
	if r, ok := d.recs[target]; ok {
		o.Build = d.labelOf[r.BundleSHA256]
	}
	d.mu.Unlock()
	d.finish(&o, st, b, err, res.Deployment)
	return o
}

// promote deploys preview alias's bundle to dpd.
func (d *deploysRun) promote(client string, rep int, alias string, keep bool) depOp {
	o := depOp{Client: client, Kind: "promote", Replica: rep, Target: alias}
	var res struct {
		PromotedFrom string
		Deployment   depView
	}
	o.Sent = time.Now()
	st, b, err := d.c.tryOn(rep, "POST", "/v1/tenants/"+d.c.tenantID+"/workloads/"+dpdWL+"/promotions",
		map[string]any{"preview": alias, "keepPreview": keep}, &res)
	o.Recv, o.Status = time.Now(), st
	if st/100 == 2 {
		d.mu.Lock()
		if r, ok := d.recs[res.PromotedFrom]; ok {
			o.Build = d.labelOf[r.BundleSHA256]
		}
		d.mu.Unlock()
	}
	d.finish(&o, st, b, err, res.Deployment)
	return o
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func digestOf(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:8])
}

// putSecret writes a fresh value of name on workload wl through replica rep.
func (d *deploysRun) putSecret(client string, rep int, wl, name string) secOp {
	d.mu.Lock()
	k := wl + "/" + name
	d.nextVer[k]++
	ver := d.nextVer[k]
	val := fmt.Sprintf("dpsec-%s-%s-v%d-%s", d.runID, name, ver, randHex(8))
	dg := digestOf(val)
	d.vals[dg] = secretVal{name, ver, val}
	d.dh.h.mu.Lock()
	d.dh.h.log("secval", map[string]any{"digest": dg, "name": name, "ver": ver}) // never the value
	d.dh.h.mu.Unlock()
	d.mu.Unlock()
	o := secOp{Client: client, Workload: wl, Name: name, Kind: "put", Ver: ver, Digest: dg, Replica: rep, Sent: time.Now()}
	st, b, err := d.c.tryOn(rep, "PUT", "/v1/tenants/"+d.c.tenantID+"/workloads/"+wl+"/secrets/"+name, map[string]any{"value": val}, nil)
	o.Recv, o.Status = time.Now(), st
	if err != nil {
		o.Err = err.Error()
	} else if st/100 != 2 {
		o.Err = bodyText(b)
	}
	d.dh.sec(o)
	return o
}

func (d *deploysRun) deleteSecret(client string, rep int, wl, name string) secOp {
	o := secOp{Client: client, Workload: wl, Name: name, Kind: "delete", Digest: "-", Replica: rep, Sent: time.Now()}
	st, b, err := d.c.tryOn(rep, "DELETE", "/v1/tenants/"+d.c.tenantID+"/workloads/"+wl+"/secrets/"+name, nil, nil)
	o.Recv, o.Status = time.Now(), st
	if err != nil {
		o.Err = err.Error()
	} else if st/100 != 2 {
		o.Err = bodyText(b)
	}
	d.dh.sec(o)
	return o
}

// waitSettled polls a deployment until it is active or failed, or 30 s.
func (d *deploysRun) waitSettled(rep int, id string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var v depView
		st, _, err := d.c.tryOn(rep, "GET", "/v1/deployments/"+id, nil, &v)
		if err == nil && st == 200 && (v.Status == "active" || v.Status == "failed" || v.Status == "superseded" || v.Status == "stopped") {
			return
		}
		if err != nil {
			rep = (rep + 1) % d.c.replicas()
		}
		select {
		case <-d.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (d *deploysRun) sleep(rnd *mrand.Rand, lo, hi time.Duration) bool {
	select {
	case <-d.stop:
		return false
	case <-time.After(lo + time.Duration(rnd.Int63n(int64(hi-lo)+1))):
		return true
	}
}

// rollbackTarget picks an earlier dpd deployment that ran and whose bundle
// is retained (not the newest), or "".
func (d *deploysRun) rollbackTarget(rnd *mrand.Rand) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var rs []depRec
	for _, r := range d.recs {
		if r.Workload == dpdWL {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Epoch > rs[j].Epoch })
	var cands []string
	for i, r := range rs {
		if i == 0 || r.ActiveAt == 0 || !r.BundleRetained || r.BundleSHA256 == "" {
			continue
		}
		cands = append(cands, r.DeploymentID)
		if len(cands) == 6 {
			break
		}
	}
	if len(cands) == 0 {
		return ""
	}
	return cands[rnd.Intn(len(cands))]
}

// deployer is one deploy client: deploys, rollbacks, preview deploys and
// promotions, one at a time, mostly through its own replica.
func (d *deploysRun) deployer(name string, home int, seed int64) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		rnd := mrand.New(mrand.NewSource(seed))
		rep := home % d.c.replicas()
		for d.sleep(rnd, time.Second, 4*time.Second) {
			label := d.bundles[rnd.Intn(len(d.bundles))].label
			var o depOp
			switch p := rnd.Intn(100); {
			case p < 35:
				o = d.deploy(name, rep, label, "", "", false, false)
			case p < 65:
				t := d.rollbackTarget(rnd)
				if t == "" {
					continue
				}
				o = d.rollback(name, rep, t, "", false, false)
			case p < 85:
				o = d.deploy(name, rep, label, previewAliases[rnd.Intn(len(previewAliases))], "", false, false)
			default:
				o = d.promote(name, rep, previewAliases[rnd.Intn(len(previewAliases))], rnd.Intn(2) == 0)
			}
			if o.Status == 0 { // the replica is down: use the next one
				rep = (rep + 1) % d.c.replicas()
				continue
			}
			if o.DeploymentID != "" {
				d.waitSettled(rep, o.DeploymentID)
			}
			rep = home % d.c.replicas()
		}
	}()
}

// secretWriter puts and deletes the given names, one op at a time.
func (d *deploysRun) secretWriter(name string, home int, seed int64, names []string, previewP bool) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		rnd := mrand.New(mrand.NewSource(seed))
		rep := home % d.c.replicas()
		for d.sleep(rnd, 700*time.Millisecond, 3*time.Second) {
			var o secOp
			switch p := rnd.Intn(100); {
			case previewP && p < 10:
				o = d.putSecret(name, rep, previewWL(previewAliases[rnd.Intn(len(previewAliases))]), "SEC_P")
			case p < 82:
				o = d.putSecret(name, rep, dpdWL, names[rnd.Intn(len(names))])
			default:
				o = d.deleteSecret(name, rep, dpdWL, names[rnd.Intn(len(names))])
			}
			if o.Status == 0 {
				rep = (rep + 1) % d.c.replicas()
			} else {
				rep = home % d.c.replicas()
			}
		}
	}()
}

// probe asks every endpoint ever seen for /info.
func (d *deploysRun) prober() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		tk := time.NewTicker(400 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-tk.C:
			}
			d.mu.Lock()
			eps := map[string]endpoint{}
			for k, v := range d.eps {
				eps[k] = v
			}
			d.mu.Unlock()
			for dns, ep := range eps {
				go d.probe(dns, ep)
			}
		}
	}()
}

func (d *deploysRun) probe(dns string, ep endpoint) infoObs {
	o := infoObs{Workload: ep.workload, DNS: dns, Host: ep.host, Sent: time.Now()}
	var r struct {
		Epoch               int64
		Inst, Build, WBuild string
		Sec, WSec           map[string]string
	}
	_, err := getJSON(d.cli, "http://"+dns+"/info", &r)
	o.Recv, o.OK = time.Now(), err == nil
	if err != nil {
		o.Err = err.Error()
	} else {
		o.Epoch, o.Inst, o.Build, o.WBuild, o.Sec, o.WSec = r.Epoch, r.Inst, r.Build, r.WBuild, r.Sec, r.WSec
	}
	d.dh.ob(o)
	return o
}

// history lists a workload's whole deployment history through replica
// rep; ok is false unless every page came back.
func (d *deploysRun) history(rep int, wl string) ([]depRec, bool) {
	var out []depRec
	cursor := ""
	for {
		var pg struct {
			Items      []depRec
			NextCursor *string
		}
		p := "/v1/tenants/" + d.c.tenantID + "/workloads/" + wl + "/deployments?limit=100"
		if cursor != "" {
			p += "&cursor=" + url.QueryEscape(cursor)
		}
		st, _, err := d.c.tryOn(rep, "GET", p, nil, &pg)
		if st == 404 && cursor == "" {
			return nil, true
		}
		if err != nil || st != 200 {
			return out, false
		}
		out = append(out, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return out, true
		}
		cursor = *pg.NextCursor
	}
}

// watch polls every workload's history: new records, changes to fields
// that never change (V5), records that vanish (V5), and the newest dpd
// deployment (the nemesis's target and the probes' endpoints).
func (d *deploysRun) watch() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-tk.C:
			}
			d.poll(d.replica())
		}
	}()
}

func (d *deploysRun) poll(rep int) {
	for _, wl := range append([]string{dpdWL}, previewWL(previewAliases[0]), previewWL(previewAliases[1])) {
		rs, complete := d.history(rep, wl)
		seen := map[string]bool{}
		var newest depRec
		for _, r := range rs {
			seen[r.DeploymentID] = true
			if r.Epoch > newest.Epoch {
				newest = r
			}
			d.mu.Lock()
			old, known := d.recs[r.DeploymentID]
			d.recs[r.DeploymentID] = r
			d.mu.Unlock()
			switch {
			case !known:
				d.dh.rec(recEv{At: time.Now(), Kind: "new", Rec: r})
			default:
				if why := immutableChange(old, r); why != "" {
					d.dh.rec(recEv{At: time.Now(), Kind: why, Rec: r, Was: &old})
				}
			}
		}
		if complete {
			d.mu.Lock()
			var gone []depRec
			for id, r := range d.recs {
				if r.Workload == wl && !seen[id] {
					gone = append(gone, r)
				}
			}
			d.mu.Unlock()
			for _, r := range gone {
				d.dh.rec(recEv{At: time.Now(), Kind: "vanished", Rec: r, Detail: fmt.Sprintf("listed through r%d", rep)})
			}
		}
		if newest.DeploymentID == "" {
			continue
		}
		var v depView
		if st, _, err := d.c.tryOn(rep, "GET", "/v1/deployments/"+newest.DeploymentID, nil, &v); err == nil && st == 200 && v.Endpoint.Status == "ready" && v.Endpoint.MagicDNS != "" {
			v.Workload = wl
			d.noteEndpoint(v)
			if wl == dpdWL {
				d.tg.mu.Lock()
				d.tg.depID = v.DeploymentID
				d.tg.d = deployment{DeploymentID: v.DeploymentID, Host: v.Host, Status: v.Status, Epoch: v.Epoch}
				d.tg.d.Endpoint.MagicDNS, d.tg.d.Endpoint.Status = v.Endpoint.MagicDNS, v.Endpoint.Status
				d.tg.mu.Unlock()
			}
		}
	}
}

// immutableChange names a change to a field of a deployment record that
// must never change ("changed"), or a hash or rollbackOf that appeared
// after the record was first listed ("meta": written after the deploy
// committed).
func immutableChange(a, b depRec) string {
	switch {
	case a.Workload != b.Workload, a.Host != b.Host, a.Epoch != b.Epoch, a.CreatedAt != b.CreatedAt, a.DeployedBy != b.DeployedBy,
		a.BundleSHA256 != "" && a.BundleSHA256 != b.BundleSHA256, a.RollbackOf != "" && a.RollbackOf != b.RollbackOf,
		a.ActiveAt != 0 && a.ActiveAt != b.ActiveAt:
		return "changed"
	case a.BundleSHA256 != b.BundleSHA256, a.RollbackOf != b.RollbackOf:
		return "meta"
	}
	return ""
}

// fetchAudit reads the tenant's audit records of the given kinds.
func (d *deploysRun) fetchAudit(kinds ...string) ([]auditRec, []byte, error) {
	var out []auditRec
	var raw []byte
	cursor := ""
	for {
		q := url.Values{"tenant": {d.c.tenantID}, "limit": {"500"}, "kind": kinds}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var pg struct {
			Items      []auditRec
			NextCursor *string
		}
		var st int
		var b []byte
		var err error
		for i := 0; i < d.c.replicas()*3; i++ {
			if st, b, err = d.c.tryOn(i%d.c.replicas(), "GET", "/v1/audit?"+q.Encode(), nil, &pg); err == nil && st == 200 {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil || st != 200 {
			return out, raw, fmt.Errorf("audit: %d %s %v", st, bodyText(b), err)
		}
		raw = append(raw, b...)
		out = append(out, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return out, raw, nil
		}
		cursor = *pg.NextCursor
	}
}

// ---- the deploys faults (registered in nemesis_test.go) ----

// deployThen starts a sticky deploy of a random bundle on the current
// holder and, mid-activation (0–1.5 s after the 202), breaks the host.
func (n *nemesis) deployThen(h string, brk func()) depOp {
	d := n.dep
	o := d.deploy("nemesis", d.replica(), d.bundles[n.rnd.Intn(len(d.bundles))].label, "", "", false, false)
	time.Sleep(n.between(0, 1500*time.Millisecond))
	brk()
	return o
}

func opSummary(o depOp) string {
	if o.Status/100 == 2 {
		return fmt.Sprintf("%s %s → %s epoch %d on %s", o.Kind, o.Build, o.DeploymentID, o.Epoch, o.RHost)
	}
	return fmt.Sprintf("%s %s refused: %d %s %s", o.Kind, o.Build, o.Status, o.Code, o.Err)
}

// takeoverDeploy deploys (or, half the time, rolls back) dpd onto another
// host with --force-takeover once mgmt allows it (the old lease must run
// out first: docs/locks.md "Failure modes"), retrying refusals.
func (n *nemesis) takeoverDeploy(from string, live bool, within time.Duration) (depOp, error) {
	d := n.dep
	to := n.other(from)
	deadline := time.Now().Add(within)
	for {
		var o depOp
		if t := d.rollbackTarget(n.rnd); t != "" && n.rnd.Intn(2) == 0 {
			o = d.rollback("nemesis", d.replica(), t, to, live, true)
		} else {
			o = d.deploy("nemesis", d.replica(), d.bundles[n.rnd.Intn(len(d.bundles))].label, "", to, live, true)
		}
		if o.Status/100 == 2 {
			d.waitSettled(d.replica(), o.DeploymentID)
			return o, nil
		}
		if time.Now().After(deadline) {
			return o, fmt.Errorf("takeover to %s: %s", to, opSummary(o))
		}
		time.Sleep(time.Second)
	}
}

func faultDeployKillHost(n *nemesis, h string) string {
	down := n.between(time.Second, 5*time.Second)
	o := n.deployThen(h, func() { n.c.stop(h, true) })
	time.Sleep(down)
	n.c.startHost(h)
	n.c.waitHostsLive(h)
	return fmt.Sprintf("%s; host SIGKILLed mid-activation, down %v", opSummary(o), down)
}

func faultDeployPauseHost(n *nemesis, h string) string {
	p := n.between(3*time.Second, 20*time.Second)
	o := n.deployThen(h, func() { n.c.pause(h, false) })
	time.Sleep(p)
	n.c.resume(h)
	return fmt.Sprintf("%s; host SIGSTOPped mid-activation for %v", opSummary(o), p)
}

func faultDeployCut(n *nemesis, h string) string {
	p := n.between(3*time.Second, 20*time.Second)
	o := n.deployThen(h, func() { n.c.proxies[h].cut(true) })
	time.Sleep(p)
	n.c.proxies[h].cut(false)
	return fmt.Sprintf("%s; host cut off from mgmt mid-activation for %v", opSummary(o), p)
}

func faultDeployPauseTakeover(n *nemesis, h string) string {
	return n.deployTakeover(h, func() { n.c.pause(h, false) }, func() { n.c.resume(h) })
}

func faultDeployCutTakeover(n *nemesis, h string) string {
	return n.deployTakeover(h, func() { n.c.proxies[h].cut(true) }, func() { n.c.proxies[h].cut(false) })
}

func (n *nemesis) deployTakeover(h string, brk, heal func()) string {
	t0 := time.Now()
	o := n.deployThen(h, brk)
	t, err := n.takeoverDeploy(h, false, 120*time.Second)
	if err != nil {
		heal()
		return "FAILED: " + err.Error()
	}
	time.Sleep(n.between(2*time.Second, 8*time.Second))
	heal()
	return fmt.Sprintf("%s; host broken mid-activation; takeover %s; healed at t0+%.1fs", opSummary(o), opSummary(t), time.Since(t0).Seconds())
}

// faultDeployMove moves the live workload with a new bundle (deploy or
// rollback --replace --force-takeover).
func faultDeployMove(n *nemesis, h string) string {
	o, err := n.takeoverDeploy(h, true, 60*time.Second)
	if err != nil {
		return "FAILED: " + err.Error()
	}
	return "live move: " + opSummary(o)
}

// faultDeployRace fires a deploy and a rollback of dpd at the same moment
// through two different replicas.
func faultDeployRace(n *nemesis, h string) string {
	d := n.dep
	t := d.rollbackTarget(n.rnd)
	label := d.bundles[n.rnd.Intn(len(d.bundles))].label
	r1 := n.rnd.Intn(d.c.replicas())
	r2 := (r1 + 1) % d.c.replicas()
	var a, b depOp
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a = d.deploy("nemesis", r1, label, "", "", false, false) }()
	go func() {
		defer wg.Done()
		if t == "" {
			b = d.deploy("nemesis", r2, d.bundles[n.rnd.Intn(len(d.bundles))].label, "", "", false, false)
		} else {
			b = d.rollback("nemesis", r2, t, "", false, false)
		}
	}()
	wg.Wait()
	time.Sleep(3 * time.Second)
	return fmt.Sprintf("r%d %s | r%d %s", r1, opSummary(a), r2, opSummary(b))
}

// faultDeployKillMgmt sends a deploy (or rollback) through one replica and
// SIGKILLs that replica 0–300 ms later, mid-request; it restarts 1–3 s
// later. On SQLite it is mgmt itself.
func faultDeployKillMgmt(n *nemesis, h string) string {
	d := n.dep
	rep := n.rnd.Intn(d.c.replicas())
	name := "mgmt"
	if d.c.pg != nil {
		name = replicaName(rep)
	}
	t := d.rollbackTarget(n.rnd)
	label := d.bundles[n.rnd.Intn(len(d.bundles))].label
	done := make(chan depOp, 1)
	go func() {
		if t != "" && n.rnd.Intn(2) == 0 {
			done <- d.rollback("nemesis", rep, t, "", false, false)
		} else {
			done <- d.deploy("nemesis", rep, label, "", "", false, false)
		}
	}()
	time.Sleep(n.between(0, 300*time.Millisecond))
	n.c.stop(name, true)
	o := <-done
	time.Sleep(n.between(time.Second, 3*time.Second))
	n.c.startMgmt(false)
	return fmt.Sprintf("%s SIGKILLed mid-request: %s", name, opSummary(o))
}

func faultPG(n *nemesis, h string) string { return n.pgFault() }

// ---- the test ----

// testDeploys runs a deploys scenario (TestDataplane hands it every
// TW_DP_SCENARIO that starts with "deploys").
func testDeploys(t *testing.T, scenario string, names []string, seed int64, dur time.Duration) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-dpd-")
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

	c := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}}
	if os.Getenv("TW_DP_MGMT") == "" { // through different replicas: 3 on Postgres unless asked otherwise
		c.pg = &mgmtPG{n: 3}
	}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}

	cli, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "dp-cli", filepath.Join(work, "dp-cli"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	h, err := newHistory(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()

	keyFile := filepath.Join(work, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	c.startMgmt(true)
	t.Logf("mgmt: %d replica(s)", c.replicas())
	var tn struct{ TenantID string }
	c.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	c.tenantID = tn.TenantID
	for _, id := range c.hosts {
		c.proxies[id] = startProxy(t, filepath.Join(work, id+"-mgmt.sock"), c.sock)
		c.startHost(id)
	}
	c.waitHostsLive(c.hosts...)

	d := &deploysRun{c: c, dh: &dhist{h: h}, tg: &target{seen: map[string]string{}}, cli: cli.HTTPClient(3 * time.Second),
		runID: randHex(4), stop: make(chan struct{}), labelOf: map[string]string{}, vals: map[string]secretVal{},
		nextVer: map[string]int{}, eps: map[string]endpoint{}, recs: map[string]depRec{}}
	app, _ := filepath.Abs(filepath.Join("testdata", "deploys"))
	for _, label := range []string{"b1", "b2", "b3", "b4"} {
		files, err := hostdtest.Build(app, map[string]string{"BUILD": label})
		if err != nil {
			t.Fatal(err)
		}
		d.bundles = append(d.bundles, dBundle{label: label, files: files}) // each deploy makes its own from these (bundle)
	}

	// The workload's secrets before the first deploy, then b1 on h1.
	for _, n := range secretNames {
		if o := d.putSecret("init", 0, dpdWL, n); o.Status/100 != 2 {
			t.Fatalf("secret put %s: %d %s", n, o.Status, o.Err)
		}
	}
	first := d.deploy("init", 0, "b1", "", "h1", false, false)
	if first.Status != 202 {
		c.dumpLogs()
		t.Fatalf("deploy: %d %s", first.Status, first.Err)
	}
	dd, err := c.waitActive(first.DeploymentID, 120*time.Second)
	if err != nil {
		c.dumpLogs()
		t.Fatal(err)
	}
	d.tg.set(dd)
	for i := 0; ; i++ {
		if o := d.probe(dd.Endpoint.MagicDNS, endpoint{dpdWL, dd.Host}); o.OK {
			break
		}
		if i > 150 {
			c.dumpLogs()
			t.Fatal("dpd never answered")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("dpd on %s at epoch %d (%s)", dd.Host, dd.Epoch, dd.Endpoint.MagicDNS)

	d.watch()
	d.prober()
	d.deployer("dA", 0, seed*10+1)
	d.deployer("dB", 1, seed*10+2)
	d.deployer("dC", 2, seed*10+3)
	d.secretWriter("sA", 1, seed*10+4, []string{"SEC_A", "SEC_C"}, false)
	d.secretWriter("sB", 2, seed*10+5, []string{"SEC_B", "SEC_C"}, true)

	time.Sleep(10 * time.Second)
	n := &nemesis{c: c, h: h, tg: d.tg, rnd: mrand.New(mrand.NewSource(seed)), dep: d}
	n.run(names, time.Now().Add(dur), t.Logf)

	// Heal: every host resumed, reconnected and running; every replica up
	// with a clean path to Postgres.
	if c.pg != nil {
		for _, px := range c.pg.proxies {
			px.Set("pass", 0)
		}
	}
	c.startMgmt(false)
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	t.Logf("healed; letting the clients run 15 s more")
	time.Sleep(15 * time.Second)
	close(d.stop)
	d.wg.Wait()
	time.Sleep(2 * time.Second)

	// V6: what serves after the faults stop.
	v6 := d.converge(healed)
	// The final read: a redeploy after every secret write has finished
	// must see the last acknowledged value of each (V4).
	final := d.finalRead()

	audit, rawAudit, aerr := d.fetchAudit("deploy", "rollback", "promote", "secret_release", "secret_put", "secret_delete", "deny")
	d.dh.audit = audit
	h.log("audit", audit)
	scan := d.scanForValues(rdir, rawAudit)

	h.mu.Lock()
	res := []result{checkV1(d), checkV2(d), checkV3(d), checkV4(d, final, scan), checkV5(d, aerr), v6}
	writeJSON(rdir, "faults.json", h.faults)
	h.mu.Unlock()
	writeJSON(rdir, "report.json", map[string]any{"scenario": scenario, "seed": seed, "duration": dur.String(), "mgmtReplicas": c.replicas(), "results": res})
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

// converge (V6): after healing, each workload's newest deployment is
// active and serves its bundle at its epoch within 60 s; on dpd it is the
// newest acknowledged (or unknown-outcome) deploy or rollback.
func (d *deploysRun) converge(healed time.Time) result {
	r := result{ID: "V6", Pass: true}
	d.poll(0)
	d.mu.Lock()
	newest := map[string]depRec{}
	for _, x := range d.recs {
		if x.Epoch > newest[x.Workload].Epoch {
			newest[x.Workload] = x
		}
	}
	d.mu.Unlock()
	var parts []string
	for _, wl := range sortedKeys(newest) {
		x := newest[wl]
		if x.Status == "stopped" {
			parts = append(parts, wl+": stopped")
			continue
		}
		dl, err := d.c.waitActive(x.DeploymentID, 60*time.Second)
		if err != nil {
			r.failUnlessKnown("%s: newest deployment %s (epoch %d) not active 60 s after healing: %v", wl, x.DeploymentID, x.Epoch, err)
			continue
		}
		want := d.labelOf[x.BundleSHA256]
		by := time.Now().Add(60 * time.Second)
		var o infoObs
		for {
			o = d.probe(dl.Endpoint.MagicDNS, endpoint{wl, dl.Host})
			if o.OK && o.Epoch == x.Epoch && o.Build == want {
				break
			}
			if time.Now().After(by) {
				r.failUnlessKnown("%s: not serving %s at epoch %d on %s 60 s after it was active: last answer epoch %d build %q %s", wl, want, x.Epoch, dl.Host, o.Epoch, o.Build, o.Err)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		parts = append(parts, fmt.Sprintf("%s: %s epoch %d on %s", wl, want, x.Epoch, dl.Host))
	}
	r.Summary = fmt.Sprintf("converged %.1fs after healing: %s", time.Since(healed).Seconds(), strings.Join(parts, "; "))
	return r
}

// finalRead redeploys dpd's current bundle after every secret write has
// finished and returns what it serves.
func (d *deploysRun) finalRead() infoObs {
	cur := d.tg.get()
	var label string
	d.mu.Lock()
	if x, ok := d.recs[cur.DeploymentID]; ok {
		label = d.labelOf[x.BundleSHA256]
	}
	d.mu.Unlock()
	if label == "" {
		label = "b1"
	}
	var o depOp
	for i := 0; i < 10; i++ {
		if o = d.deploy("final", i%d.c.replicas(), label, "", "", false, false); o.Status == 202 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if o.Status != 202 {
		return infoObs{Err: "final deploy: " + opSummary(o)}
	}
	dl, err := d.c.waitActive(o.DeploymentID, 90*time.Second)
	if err != nil {
		return infoObs{Err: err.Error()}
	}
	d.poll(0)
	var ob infoObs
	for i := 0; i < 120; i++ {
		if ob = d.probe(dl.Endpoint.MagicDNS, endpoint{dpdWL, dl.Host}); ob.OK && ob.Epoch == dl.Epoch {
			return ob
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ob
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
