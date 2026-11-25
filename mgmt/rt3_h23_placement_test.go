package mgmt

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRT3_H23_PlacementPoison (security/LEDGER.md H23, #156): a hostile
// host's path reports, sent as a registered tag:tw-host node through the
// WhoIs gate (spec-validated). Two hosts run workloads of one tenant; h1 is
// hostile, h2 honest.
//
//   - h1 can't write h2's rows (so it can't make h2 look unreachable) or
//     report for a tenant it doesn't run.
//   - Out-of-range numbers don't win: an RTT so large that it overflowed
//     int64 µs into a negative ("best") total, and an age so large that it
//     overflowed time.Duration into a measuredAt in the future (fresh for
//     ever, never deleted), are clamped at ingest.
//   - Plausible fake numbers can't move a running stateful workload: a
//     plain redeploy stays sticky, --replace is refused (move_unconfirmed).
//   - Not covered: plausible fake numbers do choose the host of a NEW
//     workload of a tenant h1 already runs. Nothing can tell a fake
//     "direct 0.1 ms" from a real one, and #156 decided against adding
//     a second measurement to check it.
func TestRT3_H23_PlacementPoison(t *testing.T) {
	t.Parallel()
	a := newAuthzEnv(t)
	type sample = map[string]any
	report := func(tenant string, ss ...sample) map[string]any {
		return map[string]any{"tenants": []map[string]any{{"tenantID": tenant, "targets": ss}}}
	}
	put := func(from, host string, body map[string]any) {
		t.Helper()
		if st, eb := a.call(from, "PUT", "/v1/hosts/"+host+"/paths", body, nil); st != 200 {
			t.Fatalf("report as %s: %d %v", host, st, eb)
		}
	}
	direct := func(target string, rtt float64, age int64) sample {
		return sample{"target": target, "path": "direct", "rttMs": rtt, "ageMs": age, "peer": "p"}
	}
	type dep struct {
		Host      string
		Placement PlacementDecision
	}
	deploy := func(workload, cfg string, extra map[string]any) (dep, int, map[string]any) {
		t.Helper()
		body := map[string]any{"tenantID": a.tn, "workload": workload,
			"bundle": makeBundle(t, map[string]string{"tailworks.jsonc": cfg, "dist/index.js": "export default {}"})}
		for k, v := range extra {
			body[k] = v
		}
		var d dep
		st, eb := a.call(nodeAdm, "POST", "/v1/deployments", body, &d)
		return d, st, eb
	}
	sum := `{"placement": {"aggregate": "sum"}, "tailnet": {"tags": ["tag:w"], "connects": ["tag:db", "tag:cache"]}}`

	// 1. Scope: rows are keyed by the authenticated host; tenants by its
	// assignments.
	a.fail(nodeH1, "PUT", "/v1/hosts/h2/paths", report(a.tn, sample{"target": "tag:db", "path": "unreachable", "ageMs": 0}), 403, "forbidden")
	a.fail(nodeH1, "PUT", "/v1/hosts/h1/paths", report(a.tn2, direct("tag:db", 0.1, 0)), 403, "forbidden")

	// 2. RTT overflow: 1e300 ms per target. Before the clamp, the sum of
	// two int64(1e303) wrapped below h2's honest 10 ms.
	put(nodeH2, "h2", report(a.tn, direct("tag:db", 5, 0), direct("tag:cache", 5, 0)))
	put(nodeH1, "h1", report(a.tn, direct("tag:db", 1e300, 0), direct("tag:cache", 1e300, 0)))
	if d, st, eb := deploy("rtt", sum, nil); st/100 != 2 || d.Host != "h2" {
		t.Fatalf("huge RTT won placement: %d %v %+v", st, eb, d)
	}

	// 3. Age overflow: ageMs past MaxInt64 ns. Before the clamp, measuredAt
	// landed ~290 years ahead, so "direct 0.1 ms" never went stale.
	put(nodeH1, "h1", report(a.tn, direct("tag:db", 0.1, 9_300_000_000_000), direct("tag:cache", 0.1, 9_300_000_000_000)))
	if d, st, eb := deploy("age", sum, nil); st/100 != 2 || d.Host != "h2" {
		t.Fatalf("future measurement won placement: %d %v %+v", st, eb, d)
	}
	ms, err := a.store.HostPaths(context.Background(), a.tn, []string{"tag:db"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.HostID == "h1" && time.UnixMilli(m.MeasuredAt).After(a.m.now()) {
			t.Fatalf("h1's row is measured in the future: %v", time.UnixMilli(m.MeasuredAt))
		}
	}

	// 4. Plausible lies. A stateful workload runs on h2; h1 claims the best
	// path, h2 honestly reports DERP.
	stateful := `{"tailnet": {"connects": ["tag:db"], "objects": {"Room": {"tags": ["tag:w"]}}}}`
	if d, st, eb := deploy("room", stateful, map[string]any{"host": "h2"}); st/100 != 2 || d.Host != "h2" {
		t.Fatalf("pinned stateful: %d %v %+v", st, eb, d)
	}
	put(nodeH2, "h2", report(a.tn, sample{"target": "tag:db", "path": "derp", "rttMs": 80, "ageMs": 0}))
	put(nodeH1, "h1", report(a.tn, direct("tag:db", 0.1, 0)))
	if d, st, eb := deploy("room", stateful, nil); st/100 != 2 || d.Host != "h2" || d.Placement.Reason != "sticky" {
		t.Fatalf("plain redeploy moved the stateful workload: %d %v %+v", st, eb, d)
	}
	if _, st, eb := deploy("room", stateful, map[string]any{"replace": true}); st != 409 || eb["code"] != "move_unconfirmed" {
		t.Fatalf("--replace moved the stateful workload without confirmation: %d %v", st, eb)
	}
	// Disclosed: a new workload goes where the (unverifiable) report says.
	if d, st, eb := deploy("room2", stateful, nil); st/100 != 2 || d.Host != "h1" || d.Placement.Reason != "smart" ||
		!strings.Contains(d.Placement.Summary, "h2: relayed") {
		t.Fatalf("new workload: %d %v %+v", st, eb, d)
	}
}

// TestRT3_H23_StoredRowsBounded: rows stored before the ingest clamp (a
// negative or huge RTT, a measuredAt in the future) are not scored.
func TestRT3_H23_StoredRowsBounded(t *testing.T) {
	t.Parallel()
	db := []string{"tag:db"}
	cands := []Candidate{cand("h1", 1), cand("h2", 1)}
	honest := meas("h2", "tag:db", PathDirect, 5, 0)
	for name, bad := range map[string]PathMeasurement{
		"negative RTT": {HostID: "h1", Target: "tag:db", Path: PathDirect, RTTMicros: -1 << 62, MeasuredAt: t0.UnixMilli()},
		"future":       {HostID: "h1", Target: "tag:db", Path: PathDirect, RTTMicros: 100, MeasuredAt: t0.Add(100 * 365 * 24 * time.Hour).UnixMilli()},
	} {
		d := scoreHosts(cands, []PathMeasurement{bad, honest}, Requirements{Targets: db, Now: t0})
		if d.Host != "h2" {
			t.Errorf("%s: placed on %s (%s)", name, d.Host, d.Summary)
		}
	}
}
