//go:build e2evm && (linux || darwin)

package vm

import (
	"context"
	"testing"
	"time"
)

// TestE2EVMCron: cron triggers on the microVM tier (e2e/vm/testdata/vmcron).
// hostd runs scheduled() in the guest's workerd: once on demand (RunCron,
// what `tw cron trigger` and the scheduler both end in), and from the
// host's own scheduler loop on the real clock ("* * * * *").
func TestE2EVMCron(t *testing.T) {
	s := startVMSuite(t, nil)
	base := s.deploy(vmApp("vmcron"), "vmcron", nil)
	runs := func() []map[string]any {
		code, v := s.do("GET", base+"/runs", nil)
		m, _ := v.(map[string]any)
		if code != 200 || m == nil {
			t.Fatalf("/runs: %d %v", code, v)
		}
		var out []map[string]any
		for _, r := range m["runs"].([]any) {
			out = append(out, r.(map[string]any))
		}
		return out
	}
	find := func(cron string) map[string]any {
		for _, r := range runs() {
			if r["cron"] == cron {
				return r
			}
		}
		return nil
	}

	at := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := s.e.Host.RunCron(context.Background(), s.e.TenantID, "vmcron", "0 0 1 1 *", at)
	if err != nil || res.Outcome != "ok" {
		t.Fatalf("RunCron: %+v %v", res, err)
	}
	if r := find("0 0 1 1 *"); r == nil || r["scheduledTime"] != float64(at.UnixMilli()) || r["hasTailnet"] != true {
		t.Fatalf("on-demand run: %v", runs())
	}

	// The scheduler loop fires "* * * * *" at the next minute boundary.
	t0 := time.Now()
	s.eventually(80*time.Second, "a scheduled \"* * * * *\" run", func() bool { return find("* * * * *") != nil })
	r := find("* * * * *")
	if st := time.UnixMilli(int64(r["scheduledTime"].(float64))); st.Second() != 0 || st.Before(t0.Add(-time.Minute)) {
		t.Fatalf("scheduled run: %v", r)
	}
	t.Logf("scheduler run in the guest after %v: %v", time.Since(t0).Round(time.Second), r)
}
