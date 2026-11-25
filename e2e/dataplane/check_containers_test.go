//go:build e2edataplane && e2edataplanevm

package dataplane

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The containers loop's checkers (LOOP.md "Containers, pooled nodes and
// snapshot restore"): K1–K4 over the recorded history and the quiescent
// state.

type kInput struct {
	ops            []ctrOp
	reps           []ctrRep
	vms            []vmRep
	obs            []hostObs
	snaps          []mgmtSnap
	faults         []faultEv
	deps           []depEv
	final          map[string]hostObs
	finalObjs      []mgmtObj
	finalErr       error
	finalUp        map[string]ctrOp
	vmmEver        map[int]string
	crashSurvivors map[int]bool
	leftovers      []string // Linux: dead hostds' netns (#587)
	deletes        map[string]time.Time
	scans          map[string]string
	hosts          []string
}

func live(status string) bool {
	switch status {
	case "pending", "minted", "assigned", "ready":
		return true
	}
	return false
}

func firstLabel(s string) string {
	s, _, _ = strings.Cut(strings.TrimSuffix(s, "."), ".")
	return s
}

// holderWindow returns, for host h at time t, the earliest time h may still
// run a container although another host holds the lease: the later of the
// lease leaving h and the end of the last fault on h, plus ctrFenceBound. ok is
// false when h holds the lease at t.
func holderWindow(in kInput, h string, t time.Time) (until time.Time, ok bool) {
	holder, since := "", time.Time{}
	for _, d := range in.deps {
		if d.At.After(t) {
			break
		}
		if d.Host != holder {
			holder, since = d.Host, d.At
		}
	}
	if holder == "" || holder == h {
		return time.Time{}, false
	}
	from := since
	for _, f := range in.faults {
		if f.Host == h && !f.Start.After(t) && f.End.After(from) {
			from = f.End
		}
	}
	return from.Add(ctrFenceBound), true
}

// nonRedeployFault reports whether a fault other than a redeploy (or a
// vm-burst, which doesn't touch Box containers) overlaps [a, b] widened by
// slack.
func nonRedeployFault(faults []faultEv, a, b time.Time, slack time.Duration) string {
	for _, f := range faults {
		if f.Kind == "redeploy" || f.Kind == "vm-burst" {
			continue
		}
		if !f.Start.After(b.Add(slack)) && !f.End.Add(slack).Before(a) {
			return f.Kind
		}
	}
	return ""
}

// checkK1 — one container per Durable Object: no host other than the lease
// holder runs an object's container past the fence bound; a copy at an
// older epoch never answers after a newer one did; a container survives a
// redeploy (never rolls) and changes only across a fault, a move or the
// client's own destroy(); destroy() is bounded by the SIGTERM grace cap.
func checkK1(in kInput) result {
	r := result{ID: "K1", Pass: true}
	flagged := map[string]bool{}
	running := 0
	for _, ob := range in.obs {
		if ob.Down {
			continue
		}
		for _, ct := range ob.Ctrs {
			if ct.State != "running" && ct.State != "starting" {
				continue
			}
			running++
			until, other := holderWindow(in, ob.Host, ob.At)
			if !other || !ob.At.After(until) {
				continue
			}
			if key := ob.Host + "/" + ct.Obj; !flagged[key] {
				flagged[key] = true
				r.fail("%s: container of %s still %s on %s at %s, %.0fs past the fence bound (lease elsewhere) (fault: %s)",
					ct.Obj, ct.Obj, ct.State, ob.Host, ob.At.Format("15:04:05.000"), ob.At.Sub(until).Seconds(), faultAt(in.faults, ob.At))
			}
		}
	}

	byObj := map[string][]ctrOp{}
	for _, o := range in.ops {
		byObj[o.Obj] = append(byObj[o.Obj], o)
	}
	for _, o := range in.ops {
		if strings.HasPrefix(o.Err, "200 without a report") {
			r.note("%s: %s (sent %s, fault: %s)", o.Obj, o.Err, o.Sent.Format("15:04:05.000"), faultAt(in.faults, o.Sent))
		}
	}
	var stale, rolled, changes, downs int
	var maxDown int64
	for obj, ops := range byObj {
		sort.Slice(ops, func(i, j int) bool { return ops[i].Recv.Before(ops[j].Recv) })
		// Epochs only go up for ops sent after a newer epoch answered.
		var maxEp int64
		var maxAt time.Time
		for _, o := range ops {
			if o.Op != "up" || !o.OK {
				continue
			}
			if o.Epoch < maxEp && o.Sent.After(maxAt) {
				stale++
				r.fail("%s: up sent %s answered at epoch %d after epoch %d answered at %s (fault: %s)",
					obj, o.Sent.Format("15:04:05.000"), o.Epoch, maxEp, maxAt.Format("15:04:05.000"), faultAt(in.faults, o.Sent))
			}
			if o.Epoch > maxEp {
				maxEp, maxAt = o.Epoch, o.Recv
			}
		}
		// Never roll.
		var prev *ctrOp
		downSince := false
		for i := range ops {
			o := &ops[i]
			if o.Op == "down" {
				downSince = true
				downs++
				if o.OK && o.MS > maxDown {
					maxDown = o.MS
				}
				// The cap is on SIGTERM → SIGKILL. A destroy() on a host
				// that is stopped meanwhile, or of a container still
				// booting after a fault (destroy waits for the start), takes
				// longer; hostd logs the stop itself (stop_ms).
				if o.OK && time.Duration(o.MS)*time.Millisecond > ctrGrace+5*time.Second && nonRedeployFault(in.faults, o.Sent, o.Recv, 20*time.Second) == "" {
					r.fail("%s: destroy() took %d ms (grace cap %v)", obj, o.MS, ctrGrace)
				}
				continue
			}
			if o.Op != "up" || !o.OK || o.Boot == "" {
				continue
			}
			if prev != nil && o.Boot != prev.Boot {
				changes++
				if !downSince && o.Epoch == prev.Epoch {
					if f := nonRedeployFault(in.faults, prev.Recv, o.Recv, 20*time.Second); f == "" {
						rolled++
						r.fail("%s: container changed (boot %s → %s) at epoch %d between %s and %s with no fault, move or destroy (redeploy: %v)",
							obj, prev.Boot[:8], o.Boot[:8], o.Epoch, prev.Recv.Format("15:04:05.000"), o.Recv.Format("15:04:05.000"),
							faultAt(in.faults, o.Recv) == "redeploy")
					}
				}
			}
			prev, downSince = o, false
		}
	}
	r.Summary = fmt.Sprintf("%d running-container observations, %d off-lease past the bound; %d stale answers; %d container changes, %d unexplained (rolled); %d destroys, max %d ms (cap %v)",
		running, len(flagged), stale, changes, rolled, downs, maxDown, ctrGrace)
	return r
}

// checkK2 — single adoption: no two live objects share a stable node ID or
// a MagicDNS name (mgmt's view, every 3 s); a pool node goes to at most one
// object; every report reaches the recorder from the reporting object's
// own node, under the name the object read back; no node is reported by
// two objects; at quiescence nothing is stuck half-adopted.
func checkK2(in kInput) result {
	r := result{ID: "K2", Pass: true}
	dupSeen := map[string]bool{}
	poolIDs := map[string]bool{}
	adopters := map[string]map[string]bool{}
	for _, s := range in.snaps {
		bySID, byDNS := map[string][]string{}, map[string][]string{}
		for _, o := range s.Objs {
			if o.Namespace == "_pool" && o.StableNodeID != "" {
				poolIDs[o.StableNodeID] = true
			}
			if !live(o.Status) {
				continue
			}
			if o.StableNodeID != "" {
				bySID[o.StableNodeID] = append(bySID[o.StableNodeID], o.ObjectID+"("+o.Status+")")
				if o.Namespace != "_pool" {
					if adopters[o.StableNodeID] == nil {
						adopters[o.StableNodeID] = map[string]bool{}
					}
					adopters[o.StableNodeID][o.ObjectID] = true
				}
			}
			if o.MagicDNS != "" {
				byDNS[o.MagicDNS] = append(byDNS[o.MagicDNS], o.ObjectID+"("+o.Status+")")
			} else if o.Status == "ready" && !dupSeen["noname"+o.ObjectID] {
				dupSeen["noname"+o.ObjectID] = true
				r.fail("at %s %s is ready in mgmt with no MagicDNS name (fault: %s)", s.At.Format("15:04:05.000"), o.ObjectID, faultAt(in.faults, s.At))
			}
		}
		for sid, ids := range bySID {
			if len(ids) > 1 && !dupSeen["sid"+sid] {
				dupSeen["sid"+sid] = true
				r.fail("at %s live objects %v share stable node ID %s (fault: %s)", s.At.Format("15:04:05.000"), ids, sid, faultAt(in.faults, s.At))
			}
		}
		for dns, ids := range byDNS {
			if len(ids) > 1 && !dupSeen["dns"+dns] {
				dupSeen["dns"+dns] = true
				r.fail("at %s live objects %v share MagicDNS name %s (fault: %s)", s.At.Format("15:04:05.000"), ids, dns, faultAt(in.faults, s.At))
			}
		}
	}
	adopted := 0
	for sid := range poolIDs {
		if n := len(adopters[sid]); n > 1 {
			var ids []string
			for id := range adopters[sid] {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			r.fail("pool node %s adopted by %d objects: %v", sid, n, ids)
		} else if n == 1 {
			adopted++
		}
	}
	// The recorder's view.
	nodeObjs := map[string]map[string]bool{}
	misnamed := 0
	for _, rep := range in.reps {
		if rep.StableID == "" {
			r.note("report of %s at %s: the recorder could not WhoIs the sender", rep.Obj, rep.At.Format("15:04:05.000"))
			continue
		}
		if nodeObjs[rep.StableID] == nil {
			nodeObjs[rep.StableID] = map[string]bool{}
		}
		nodeObjs[rep.StableID][rep.Obj] = true
		// A Durable Object runs before its node is ready: self() then says
		// pending with no name yet, and the report waits for the node
		// (egress is held until ready, #331). The name read back once ready
		// must be the node's.
		if rep.Status == "ready" && firstLabel(rep.Node) != firstLabel(rep.MagicDNS) {
			misnamed++
			r.fail("%s reported from node %s but read back MagicDNS name %q (at %s)", rep.Obj, rep.Node, rep.MagicDNS, rep.At.Format("15:04:05.000"))
		}
	}
	for sid, objs := range nodeObjs {
		if len(objs) > 1 {
			var os []string
			for o := range objs {
				os = append(os, o)
			}
			sort.Strings(os)
			r.fail("node %s reported for %d objects: %v", sid, len(objs), os)
		}
	}
	// Quiescence: nothing half-adopted.
	if in.finalErr != nil {
		r.fail("final object list: %v", in.finalErr)
	}
	stuck := 0
	for _, o := range in.finalObjs {
		if o.Status == "pending" || o.Status == "minted" || o.Status == "assigned" {
			stuck++
			r.fail("at quiescence %s (%s/%s) is still %s", o.ObjectID, o.Namespace, o.ObjectName, o.Status)
		}
	}
	r.Summary = fmt.Sprintf("%d mgmt snapshots; %d pool nodes, %d adopted; %d reports from %d nodes, %d misnamed; %d stuck at quiescence",
		len(in.snaps), len(poolIDs), adopted, len(in.reps), len(nodeObjs), misnamed, stuck)
	return r
}

// checkK3 — uniqueness: no two containers share a boot_id and no urandom
// draw repeats; no two microVM-tier copies (warm pool, snapshot restore)
// share workerd's first randomUUID, getRandomValues or Math.random; each
// copy's egress leaves from its own node.
func checkK3(in kInput) result {
	r := result{ID: "K3", Pass: true}
	bootObj := map[string]map[string]bool{}
	rnds := map[string]string{}
	for _, o := range in.ops {
		if o.Op != "up" || !o.OK {
			continue
		}
		if bootObj[o.Boot] == nil {
			bootObj[o.Boot] = map[string]bool{}
		}
		bootObj[o.Boot][o.Obj] = true
		if prev, ok := rnds[o.Rnd]; ok && o.Rnd != "" {
			r.fail("urandom draw %s repeated (%s, then %s at %s)", o.Rnd, prev, o.Obj, o.Recv.Format("15:04:05.000"))
		}
		rnds[o.Rnd] = o.Obj
	}
	for boot, objs := range bootObj {
		if len(objs) > 1 {
			var os []string
			for o := range objs {
				os = append(os, o)
			}
			sort.Strings(os)
			r.fail("boot_id %s seen in the containers of %v", boot, os)
		}
	}
	seen := map[string]string{}
	wls := map[string]bool{}
	reported := 0
	for _, v := range in.vms {
		wls[v.WL] = true
		for _, kv := range [][2]string{{"randomUUID", v.UUID}, {"getRandomValues", v.Rnd}, {"Math.random", fmt.Sprint(v.MR)}} {
			key := kv[0] + "=" + kv[1]
			if prev, ok := seen[key]; ok && prev != v.WL {
				r.fail("%s %s drawn by both %s and %s", kv[0], kv[1], prev, v.WL)
			}
			seen[key] = v.WL
		}
		if v.Client {
			if v.RecErr != "" {
				r.note("%s's report to the recorder failed: %s", v.WL, v.RecErr)
			}
			continue
		}
		reported++
		if firstLabel(v.Node) != firstLabel(v.WL) {
			r.fail("%s's egress arrived from node %s", v.WL, v.Node)
		}
	}
	src := map[string]map[string]bool{}
	for _, ob := range in.obs {
		for _, vm := range ob.VMs {
			if src[vm.Source] == nil {
				src[vm.Source] = map[string]bool{}
			}
			src[vm.Source][vm.Workload] = true
		}
	}
	var srcs []string
	for s, w := range src {
		srcs = append(srcs, fmt.Sprintf("%s %d", s, len(w)))
	}
	sort.Strings(srcs)
	r.Summary = fmt.Sprintf("%d container boots, %d urandom draws; %d microVM copies compared, %d reports over their own egress (sources seen: %s)",
		len(bootObj), len(rnds), len(wls), reported, strings.Join(srcs, ", "))
	return r
}

// checkK4 — nothing left over at quiescence: no VMM outlives its hostd (or
// runs outside every hostd's tree), no host runs more VMMs than its
// containers, microVM workloads and pools account for, no host keeps a
// node for an object mgmt has ended, every ready pool node in mgmt runs on
// a host, and no container holds a parent's key (the strict keyscan).
func checkK4(in kInput) result {
	r := result{ID: "K4", Pass: true}
	for _, l := range in.leftovers {
		r.fail("%s: left by a hostd that is no longer running", l)
	}
	for p := range in.crashSurvivors {
		r.fail("VMM pid %d outlived its hostd's crash (still running 15 s after the restart)", p)
	}
	under := map[int]string{}
	for h, ob := range in.final {
		for _, p := range ob.VMMs {
			under[p] = h
		}
	}
	orphans := 0
	for p, h := range in.vmmEver {
		if _, ok := under[p]; ok || in.crashSurvivors[p] {
			continue
		}
		if syscall.Kill(p, 0) == nil && isVMM(p) {
			orphans++
			r.fail("VMM pid %d (last seen under %s) is running outside every hostd", p, h)
		}
	}
	// Warm pool VMs and a base being built are VMMs without a workload
	// (Firecracker only; vz has neither).
	spare := 0
	if runtime.GOOS == "linux" {
		spare = envInt("TW_DPK_VM_POOL", 1) + 1
	}
	for _, h := range in.hosts {
		ob := in.final[h]
		if ob.Down {
			r.fail("%s's status socket didn't answer at quiescence", h)
			continue
		}
		want := 0
		for _, ct := range ob.Ctrs {
			if ct.State == "running" || ct.State == "starting" {
				want++
			}
		}
		want += len(ob.VMs)
		if n := len(ob.VMMs); n > want+spare {
			r.fail("%s runs %d VMMs %v for %d containers and microVMs (+%d spare)", h, n, ob.VMMs, want, spare)
		}
	}
	mgmtStatus := map[string]mgmtObj{}
	for _, o := range in.finalObjs {
		mgmtStatus[o.ObjectID] = o
	}
	onHost := map[string]string{}
	for h, ob := range in.final {
		for _, o := range ob.Objs {
			// Only a ready object has a node up. A host that lost the
			// lease keeps its objects' records as pending (no node), to
			// rejoin if the lease comes back.
			if o.Status != "ready" {
				continue
			}
			onHost[o.ID] = h
			if m, ok := mgmtStatus[o.ID]; ok && !live(m.Status) {
				r.fail("%s still runs %s (%s/%s, %s) which mgmt has as %s", h, o.ID, o.Class, o.Name, o.Status, m.Status)
			}
		}
	}
	// An acknowledged kill ends the object (and so its node).
	idOf, killedOK := map[string]string{}, map[string]bool{}
	for _, o := range in.ops {
		if o.ObjectID != "" {
			idOf[o.Obj] = o.ObjectID
		}
		if o.Op == "kill" && o.OK {
			killedOK[o.Obj] = true
		}
	}
	for obj := range killedOK {
		if m, ok := mgmtStatus[idOf[obj]]; ok && live(m.Status) {
			r.fail("%s (%s): kill acknowledged, still %s at quiescence", obj, m.ObjectID, m.Status)
		}
	}
	leaked := 0
	for _, o := range in.finalObjs {
		if o.Namespace == "_pool" && o.Status == "ready" && onHost[o.ObjectID] == "" {
			leaked++
			r.fail("pool node %s (%s, lease host %q) is ready in mgmt but no host runs it", o.ObjectID, o.ObjectName, o.Host)
		}
	}
	// Device deletes for the objects this loop killed (observed, not yet
	// gated: ephemeral nodes may be left to control).
	killed, deleted := 0, 0
	for _, o := range in.finalObjs {
		if o.Status == "killed" && o.StableNodeID != "" {
			killed++
			if _, ok := in.deletes[o.StableNodeID]; ok {
				deleted++
			}
		}
	}
	r.note("%d killed objects with a node, %d with a device delete at the fake API so far (mgmt delays device deletes)", killed, deleted)
	scanned := 0
	for k, v := range in.scans {
		code, body, _ := strings.Cut(v, " ")
		var rep struct {
			KeyScan *struct {
				Needles int
				Found   []string
			}
		}
		if code != "200" || json.Unmarshal([]byte(body), &rep) != nil || rep.KeyScan == nil {
			if len(body) > 200 {
				body = body[:200]
			}
			r.note("keyscan %s: %s %s", k, code, body)
			continue
		}
		scanned++
		if len(rep.KeyScan.Found) > 0 {
			r.fail("keyscan %s: %v", k, rep.KeyScan.Found)
		}
	}
	if os.Getenv("TW_DPK_KEYSCAN_REQUIRED") == "1" && scanned == 0 {
		r.fail("no container was keyscanned")
	}
	r.Summary = fmt.Sprintf("%d VMMs outlived a hostd crash, %d outside every hostd; %d leaked pool nodes; %d containers keyscanned clean",
		len(in.crashSurvivors), orphans, leaked, scanned)
	return r
}
