//go:build e2edataplane

package dataplane

import (
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The deploys checkers (issue #421, LOOP.md "Deploys"): V1–V5 over the
// recorded history; V6 is checked live (deploys_test.go converge).

// recIndex is workload → epoch → the deployment record (the last one seen
// when two share an epoch; V2 reports that).
func recIndex(d *deploysRun) map[string]map[int64]depRec {
	out := map[string]map[int64]depRec{}
	for _, r := range d.recs {
		if out[r.Workload] == nil {
			out[r.Workload] = map[int64]depRec{}
		}
		out[r.Workload][r.Epoch] = r
	}
	return out
}

func okOps(d *deploysRun) map[string]depOp {
	out := map[string]depOp{}
	for _, o := range d.dh.ops {
		if o.DeploymentID != "" {
			out[o.DeploymentID] = o
		}
	}
	return out
}

func ts(t time.Time) string { return t.Format("15:04:05.000") }

// checkV1 — one active deployment per workload: once an epoch has
// answered, no older epoch answers a request sent after that, on any host.
// A live move (--replace --force-takeover) is allowed moveSlack: the old
// host stops at its next heartbeat.
func checkV1(d *deploysRun) result {
	r := result{ID: "V1", Pass: true}
	recs, ops := recIndex(d), okOps(d)
	byWL := map[string][]infoObs{}
	for _, o := range d.dh.obs {
		if o.OK && o.Epoch > 0 {
			byWL[o.Workload] = append(byWL[o.Workload], o)
		}
	}
	stale, answered := 0, 0
	for _, wl := range sortedKeys(byWL) {
		first := map[int64]time.Time{}
		for _, o := range byWL[wl] {
			if f, ok := first[o.Epoch]; !ok || o.Recv.Before(f) {
				first[o.Epoch] = o.Recv
			}
		}
		answered += len(first)
		for _, o := range byWL[wl] {
			for e, f := range first {
				if e <= o.Epoch {
					continue
				}
				allow := time.Duration(0)
				if op, ok := ops[recs[wl][e].DeploymentID]; ok && op.Replace && op.Force {
					allow = moveSlack
				}
				if o.Sent.After(f.Add(allow)) {
					stale++
					r.fail("%s: stale deployment answered: %s at epoch %d (build %s, sent %s) after epoch %d answered at %s",
						wl, o.Host, o.Epoch, o.Build, ts(o.Sent), e, ts(f))
					break
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("%d epochs answered across %d workloads, %d stale answers", answered, len(byWL), stale)
	return r
}

// checkV2 — epochs: unique and increasing with creation per workload,
// every acknowledged deploy got its own, and an epoch answers only from
// the host its deployment was placed on.
func checkV2(d *deploysRun) result {
	r := result{ID: "V2", Pass: true}
	byWL := map[string][]depRec{}
	for _, x := range d.recs {
		byWL[x.Workload] = append(byWL[x.Workload], x)
	}
	for _, wl := range sortedKeys(byWL) {
		rs := byWL[wl]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Epoch < rs[j].Epoch })
		for i := 1; i < len(rs); i++ {
			a, b := rs[i-1], rs[i]
			if a.Epoch == b.Epoch {
				r.fail("%s: %s and %s share epoch %d", wl, a.DeploymentID, b.DeploymentID, a.Epoch)
			}
			if a.CreatedAt > b.CreatedAt+2000 {
				r.fail("%s: epoch %d (%s) created %d ms after epoch %d (%s)", wl, a.Epoch, a.DeploymentID, a.CreatedAt-b.CreatedAt, b.Epoch, b.DeploymentID)
			}
		}
	}
	seen := map[string]string{}
	for _, o := range d.dh.ops {
		if o.DeploymentID == "" {
			continue
		}
		k := fmt.Sprintf("%s@%d", o.Workload, o.Epoch)
		if id, ok := seen[k]; ok && id != o.DeploymentID {
			r.fail("%s: acknowledged deployments %s and %s both got epoch %d", o.Workload, id, o.DeploymentID, o.Epoch)
		}
		seen[k] = o.DeploymentID
		if x, ok := d.recs[o.DeploymentID]; ok && x.Epoch != o.Epoch {
			r.fail("%s: acknowledged at epoch %d, recorded at %d", o.DeploymentID, o.Epoch, x.Epoch)
		}
	}
	recs := recIndex(d)
	wrong := 0
	for _, o := range d.dh.obs {
		if !o.OK || o.Epoch == 0 {
			continue
		}
		if x, ok := recs[o.Workload][o.Epoch]; ok && x.Host != o.Host {
			wrong++
			r.fail("%s: epoch %d answered by %s (sent %s), but its deployment %s is on %s", o.Workload, o.Epoch, o.Host, ts(o.Sent), x.DeploymentID, x.Host)
		}
	}
	r.Summary = fmt.Sprintf("%d deployments, %d acknowledged ops, %d answers from a host that doesn't hold the epoch", len(d.recs), len(seen), wrong)
	return r
}

// createdTimes is when each deployment was created, on the test's clock.
// mgmt stamps records with the store's clock (on Postgres the database's,
// in a container VM that can be seconds off the test's), so: the time the
// op that created it was sent (a lower bound), or, for a record no op
// acknowledged, its stamp shifted by the median offset between stamps and
// acknowledged ops, less a second.
func createdTimes(d *deploysRun) map[string]time.Time {
	ops := okOps(d)
	var offs []int64
	for id, o := range ops {
		if x, ok := d.recs[id]; ok {
			mid := o.Sent.Add(o.Recv.Sub(o.Sent) / 2)
			offs = append(offs, x.CreatedAt-mid.UnixMilli())
		}
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	var off int64
	if len(offs) > 0 {
		off = offs[len(offs)/2]
	}
	out := map[string]time.Time{}
	for id, x := range d.recs {
		if o, ok := ops[id]; ok {
			out[id] = o.Sent
		} else {
			out[id] = time.UnixMilli(x.CreatedAt - off - 1000)
		}
	}
	return out
}

// copyInfo is one copy of the Durable Object (its inst ID) as the probes
// saw it, and the deployment it belongs to.
type copyInfo struct {
	wl, host, build string
	first           time.Time // first answer received
	minEpoch        int64     // the lowest epoch it answered at (0 answers, where self() failed, don't count)
	dep             depRec    // the newest that fits (DeploymentID "" if none)
	deps            []depRec  // every deployment that fits (a rollback reuses its target's bundle)
}

// copies assigns every copy that answered to its deployment: the newest
// record of its workload on its host, created before the copy first
// answered, that runs the copy's bundle, at or below the lowest epoch the
// copy answered at. The reported epoch alone can't say: it is the
// workload's current one on the host, so the previous copy answers at the
// new epoch while a redeploy on the same host starts the new one.
func copies(d *deploysRun) map[string]*copyInfo {
	out, created := map[string]*copyInfo{}, createdTimes(d)
	for _, o := range d.dh.obs {
		if !o.OK || o.Inst == "" {
			continue
		}
		c := out[o.Inst]
		if c == nil {
			c = &copyInfo{wl: o.Workload, host: o.Host, build: o.Build, first: o.Recv, minEpoch: math.MaxInt64}
			out[o.Inst] = c
		}
		if o.Epoch > 0 {
			c.minEpoch = min(c.minEpoch, o.Epoch)
		}
		if o.Recv.Before(c.first) {
			c.first = o.Recv
		}
	}
	for _, c := range out {
		for _, x := range d.recs {
			if x.Workload != c.wl || x.Host != c.host || d.labelOf[x.BundleSHA256] != c.build ||
				created[x.DeploymentID].After(c.first) || x.Epoch > c.minEpoch {
				continue
			}
			c.deps = append(c.deps, x)
			if x.Epoch > c.dep.Epoch {
				c.dep = x
			}
		}
	}
	return out
}

// checkV3 — every answer comes from a copy of a deployment that exists,
// runs that deployment's bundle and was created before it answered (no
// bundle serves that no deployment of it was placed there to run); the
// Worker and the Durable Object run the same bundle; and on one host, once
// a newer copy has answered, no older copy answers a request sent after
// that (one copy per host). A copy answering at a newer epoch than its own
// deployment's is a handover (the previous copy serves until the new one
// is up): reported, not failed.
func checkV3(d *deploysRun) result {
	r := result{ID: "V3", Pass: true}
	recs := recIndex(d)
	known := map[string]bool{}
	for _, l := range d.labelOf {
		known[l] = true
	}
	cs := copies(d)
	bad, handovers, n, overlap, unknownEpoch := 0, 0, 0, 0, 0
	for _, o := range d.dh.obs {
		if !o.OK {
			continue
		}
		n++
		switch c := cs[o.Inst]; {
		case o.Build != o.WBuild:
			bad++
			r.fail("%s on %s at epoch %d: the Durable Object runs %s, the Worker %s", o.Workload, o.Host, o.Epoch, o.Build, o.WBuild)
		case !known[o.Build]:
			bad++
			r.fail("%s on %s: build %q was never deployed", o.Workload, o.Host, o.Build)
		case c == nil || c.dep.DeploymentID == "":
			bad++
			r.fail("%s on %s answered %s at epoch %d (sent %s): no deployment of %s on %s was created before that copy first answered",
				o.Workload, o.Host, o.Build, o.Epoch, ts(o.Sent), o.Build, o.Host)
		case o.Epoch == 0:
			unknownEpoch++ // self() failed (the object not ready yet): the lock template refuses then
		case c.dep.Epoch > o.Epoch:
			bad++
			r.fail("%s on %s: a copy of %s (epoch %d) answered at the older epoch %d (sent %s)", o.Workload, o.Host, c.dep.DeploymentID, c.dep.Epoch, o.Epoch, ts(o.Sent))
		case c.dep.Epoch < o.Epoch:
			handovers++
			if handovers <= 10 {
				r.note("handover: %s on %s, the copy of %s (%s, epoch %d) answered at epoch %d (sent %s)", o.Workload, o.Host, c.dep.DeploymentID, o.Build, c.dep.Epoch, o.Epoch, ts(o.Sent))
			}
		}
		if _, ok := recs[o.Workload][o.Epoch]; !ok && o.Epoch > 0 {
			bad++
			r.fail("%s on %s answered at epoch %d (sent %s): no deployment has that epoch", o.Workload, o.Host, o.Epoch, ts(o.Sent))
		}
	}
	// One copy per host: once a copy of a newer deployment answered, an
	// older copy on the same host must not answer a request sent after.
	for _, o := range d.dh.obs {
		c := cs[o.Inst]
		if !o.OK || c == nil || c.dep.DeploymentID == "" {
			continue
		}
		for _, c2 := range cs {
			if c2.wl == c.wl && c2.host == c.host && c2.dep.Epoch > c.dep.Epoch && o.Sent.After(c2.first) {
				overlap++
				r.fail("%s on %s: the copy of epoch %d (%s) answered a request sent at %s, after the copy of epoch %d (%s) had answered at %s",
					o.Workload, o.Host, c.dep.Epoch, c.build, ts(o.Sent), c2.dep.Epoch, c2.build, ts(c2.first))
				break
			}
		}
	}
	r.Summary = fmt.Sprintf("%d answers from %d copies, %d not from their deployment, %d from an older copy after a newer one on the same host, %d handover answers, %d without an epoch",
		n, len(cs), bad, overlap, handovers, unknownEpoch)
	return r
}

// secretWrites are dpd's writes of each secret, with the state before the
// first one (unset).
func secretWrites(d *deploysRun) map[string][]secOp {
	out := map[string][]secOp{}
	for _, n := range secretNames {
		out[n] = []secOp{{Name: n, Kind: "initial", Digest: "-", Status: 200}}
	}
	for _, o := range d.dh.secs {
		if o.Workload == dpdWL {
			out[o.Name] = append(out[o.Name], o)
		}
	}
	return out
}

// justified says why digest dg, seen by a deployment created at start and
// answered at recv, is not a value the deployment may hold: "" if some
// write of it (acknowledged, or of unknown outcome) was sent before recv
// and no acknowledged write was sent after it finished and acknowledged
// before start. Later values are allowed (a hostd restart refetches).
func justified(ws []secOp, dg string, start, recv time.Time) string {
	cands := 0
	for _, w := range ws {
		if w.Digest != dg || outcome(w.Status) == "failed" || !w.Sent.Before(recv) {
			continue
		}
		cands++
		superseded := false
		for _, w2 := range ws {
			if outcome(w2.Status) == "ok" && w2.Kind != "initial" && w2.Sent.After(w.Recv) && w2.Recv.Before(start.Add(-50*time.Millisecond)) {
				superseded = true
				break
			}
		}
		if !superseded {
			return ""
		}
	}
	if cands == 0 {
		return "never written"
	}
	return "stale"
}

// lastAcked is the newest write acknowledged before t, for evidence.
func lastAcked(ws []secOp, t time.Time) secOp {
	var l secOp
	for _, w := range ws {
		if outcome(w.Status) == "ok" && w.Recv.Before(t) && !w.Recv.Before(l.Recv) {
			l = w
		}
	}
	return l
}

// checkV4 — secrets: an answer only carries values the workload had when
// its deployment started (or later), never a value overwritten or deleted
// (and acknowledged) before that; the Worker and the Durable Object agree;
// a preview gets the workload's values and never those put on its own
// name; every release went to the host holding that workload's epoch;
// after every write has finished, a redeploy sees the last acknowledged
// value of each; and no value appears in any log, audit record or mgmt's
// database.
func checkV4(d *deploysRun, final infoObs, scan []string) result {
	r := result{ID: "V4", Pass: true}
	recs := recIndex(d)
	ws := secretWrites(d)
	checked, bad, cs, created := 0, 0, copies(d), createdTimes(d)
	what := func(n, dg string) string {
		if dg == "-" {
			return n + " unset"
		}
		if v, ok := d.vals[dg]; ok {
			return fmt.Sprintf("%s v%d", n, v.ver)
		}
		return n + " " + dg
	}
	for _, o := range d.dh.obs {
		if !o.OK {
			continue
		}
		c := cs[o.Inst]
		if c == nil || c.dep.DeploymentID == "" {
			continue // V3
		}
		x := c.dep
		checked++
		start := created[x.DeploymentID]
		for _, n := range secretNames {
			dg := o.Sec[n]
			if o.WSec[n] != dg {
				bad++
				r.fail("%s epoch %d: the Worker sees %s, the Durable Object %s", o.Workload, o.Epoch, what(n, o.WSec[n]), what(n, dg))
			}
			why := justified(ws[n], dg, start, o.Recv)
			for _, y := range c.deps { // a rollback's copy: any deployment of its bundle there
				if why != "" && justified(ws[n], dg, created[y.DeploymentID], o.Recv) == "" {
					why = ""
				}
			}
			if why != "" {
				bad++
				l := lastAcked(ws[n], start)
				r.fail("%s on %s, copy of %s epoch %d (created %s, answered %s): %s is %s; last acknowledged before it was created: %s at %s",
					o.Workload, o.Host, x.DeploymentID, x.Epoch, ts(start), ts(o.Recv), what(n, dg), why, what(n, l.Digest), ts(l.Recv))
			}
		}
		if dg := o.Sec["SEC_P"]; dg != "-" {
			bad++
			r.fail("%s on %s epoch %d: SEC_P (set only on a preview's own name) was released: %s", o.Workload, o.Host, o.Epoch, what("SEC_P", dg))
		}
	}
	finalOK := "final read failed: " + final.Err
	if final.OK {
		finalOK = "final read sees the last acknowledged values"
		x, ok := recs[dpdWL][final.Epoch]
		start := time.Now()
		if ok {
			start = created[x.DeploymentID]
		}
		for _, n := range secretNames {
			if why := justified(ws[n], final.Sec[n], start, final.Recv); why != "" {
				finalOK = "final read lost an acknowledged write"
				l := lastAcked(ws[n], time.Now())
				r.fail("final read: %s is %s (%s); the last acknowledged write was %s (sent %s, acknowledged %s, through r%d)",
					n, what(n, final.Sec[n]), why, what(n, l.Digest), ts(l.Sent), ts(l.Recv), l.Replica)
			}
		}
	} else {
		r.fail("final read: %s", final.Err)
	}
	rel, denied := 0, 0
	for _, a := range d.dh.audit {
		switch {
		case a.Kind == "deny" && a.Code == "lease_lost":
			if _, ok := a.Detail["secrets"]; ok {
				denied++
			}
		case a.Kind == "secret_release":
			rel++
			wl, _ := a.Detail["workload"].(string)
			host, _ := a.Detail["host"].(string)
			ep, _ := a.Detail["epoch"].(float64)
			x, ok := recs[wl][int64(ep)]
			switch {
			case !ok:
				r.fail("secret release to %s for %s at epoch %d (%s): no such deployment", host, wl, int64(ep), ts(a.Ts))
			case x.Host != host:
				r.fail("secret release to %s for %s at epoch %d (%s): that epoch's deployment %s is on %s", host, wl, int64(ep), ts(a.Ts), x.DeploymentID, x.Host)
			}
			if wl != dpdWL {
				if of, _ := a.Detail["previewOf"].(string); of != dpdWL {
					r.fail("secret release for preview %s at epoch %d: previewOf %q, want %s", wl, int64(ep), of, dpdWL)
				}
			}
		}
	}
	for _, s := range scan {
		r.fail("secret value found: %s", s)
	}
	r.Summary = fmt.Sprintf("%d answers checked, %d bad; %s; %d releases (all to the epoch's holder unless listed), %d refused with lease_lost; %d secret writes; %d value leaks",
		checked, bad, finalOK, rel, denied, len(d.dh.secs), len(scan))
	return r
}

// checkV5 — history: records never vanish and never change; a rollback
// redeploys exactly the deployment the client named; every acknowledged op
// is in the history and every record comes from an op; every record has
// its audit record of the right kind.
func checkV5(d *deploysRun, aerr error) result {
	r := result{ID: "V5", Pass: true}
	meta := 0
	for _, e := range d.dh.recs {
		switch e.Kind {
		case "changed":
			r.fail("record %s changed: was %+v, now %+v", e.Rec.DeploymentID, *e.Was, e.Rec)
		case "vanished":
			r.fail("record %s (%s epoch %d) vanished from the history at %s (%s)", e.Rec.DeploymentID, e.Rec.Workload, e.Rec.Epoch, ts(e.At), e.Detail)
		case "meta":
			meta++
			r.note("record %s got its bundle hash or rollbackOf only after it was listed (%q → %q, %q → %q)",
				e.Rec.DeploymentID, e.Was.BundleSHA256, e.Rec.BundleSHA256, e.Was.RollbackOf, e.Rec.RollbackOf)
		}
	}
	ids := map[string]bool{}
	unknown := map[string]int{}
	for _, o := range d.dh.ops {
		if o.DeploymentID != "" {
			ids[o.DeploymentID] = true
		}
		if outcome(o.Status) == "unknown" {
			wl := dpdWL
			if o.Alias != "" {
				wl = previewWL(o.Alias)
			}
			unknown[wl]++
		}
		if o.Status/100 != 2 {
			continue
		}
		x, ok := d.recs[o.DeploymentID]
		if !ok {
			r.fail("acknowledged %s %s (%s) is not in the history", o.Kind, o.DeploymentID, o.Client)
			continue
		}
		switch o.Kind {
		case "rollback":
			t, tok := d.recs[o.Target]
			switch {
			case o.RollbackOf != o.Target:
				r.fail("rollback to %s answered rollbackOf %s", o.Target, o.RollbackOf)
			case x.RollbackOf != o.Target:
				r.fail("rollback %s to %s is recorded with rollbackOf %q", x.DeploymentID, o.Target, x.RollbackOf)
			case !tok || t.BundleSHA256 != x.BundleSHA256:
				r.fail("rollback %s to %s redeployed bundle %.12s, the target's is %.12s", x.DeploymentID, o.Target, x.BundleSHA256, t.BundleSHA256)
			}
		case "deploy", "preview":
			if got := d.labelOf[x.BundleSHA256]; got != o.Build {
				r.fail("%s %s of %s is recorded with bundle %q (%.12s)", o.Kind, x.DeploymentID, o.Build, got, x.BundleSHA256)
			}
		}
	}
	phantom := map[string]int{}
	for _, x := range d.recs {
		if x.BundleSHA256 == "" {
			r.fail("record %s (%s epoch %d) has no bundle hash", x.DeploymentID, x.Workload, x.Epoch)
		}
		if !ids[x.DeploymentID] {
			phantom[x.Workload]++
		}
	}
	for wl, n := range phantom {
		if n > unknown[wl] {
			r.fail("%s: %d records that no op acknowledged, but only %d ops had an unknown outcome", wl, n, unknown[wl])
		}
	}
	if aerr != nil {
		r.fail("audit: %v", aerr)
	}
	kinds := map[string]auditRec{}
	for _, a := range d.dh.audit {
		if a.Kind != "deploy" && a.Kind != "rollback" && a.Kind != "promote" {
			continue
		}
		if id, _ := a.Detail["deploymentID"].(string); id != "" {
			kinds[id] = a
		}
	}
	for _, x := range d.recs {
		a, ok := kinds[x.DeploymentID]
		switch {
		case aerr != nil:
		case !ok:
			r.fail("record %s (%s epoch %d) has no deploy, rollback or promote audit record", x.DeploymentID, x.Workload, x.Epoch)
		case x.RollbackOf != "" && a.Kind != "rollback":
			r.fail("record %s is a rollback of %s but is audited as %s", x.DeploymentID, x.RollbackOf, a.Kind)
		case a.Kind == "rollback" && a.Detail["rollbackOf"] != x.RollbackOf:
			r.fail("record %s: audited rollbackOf %v, recorded %q", x.DeploymentID, a.Detail["rollbackOf"], x.RollbackOf)
		}
	}
	r.Summary = fmt.Sprintf("%d records, %d ops, %d audit records; %d hashes written late", len(d.recs), len(d.dh.ops), len(kinds), meta)
	return r
}

// scanForValues looks for every secret value the run wrote in the run's
// artifacts (process logs, history), the audit records, and mgmt's store
// (pg_dump, or the SQLite state dir). Hosts' instance directories hold
// the values by design and are not scanned.
func (d *deploysRun) scanForValues(rdir string, audit []byte) []string {
	var hits []string
	look := func(where string, b []byte) {
		for _, v := range d.vals {
			if bytes.Contains(b, []byte(v.value)) {
				hits = append(hits, fmt.Sprintf("%s v%d in %s", v.name, v.ver, where))
			}
		}
	}
	walk := func(root string) {
		filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil {
				look(p, b)
			}
			return nil
		})
	}
	walk(rdir)
	look("the audit records", audit)
	if d.c.pg != nil {
		if b, err := d.c.pg.pg.Dump(); err == nil {
			look("mgmt's Postgres (pg_dump)", b)
		} else {
			d.dh.h.log("note", "pg_dump failed: "+err.Error())
		}
	} else {
		walk(filepath.Join(d.c.work, "mgmt"))
	}
	return hits
}
