//go:build e2edataplane

package dataplane

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestReplay re-runs the checkers on a recorded run (TW_DP_REPLAY=<results
// dir>), so a checker change can be tried on old histories without a new
// run. D4 and D5 need the live cluster and are left out.
func TestReplay(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" {
		t.Skip("TW_DP_REPLAY not set")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := &history{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var line struct {
			Type string
			Ev   json.RawMessage
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		dec := func(v any) {
			if err := json.Unmarshal(line.Ev, v); err != nil {
				t.Fatal(err)
			}
		}
		switch line.Type {
		case "send":
			var o sendOp
			dec(&o)
			h.sends = append(h.sends, o)
		case "delivery":
			var o delivery
			dec(&o)
			h.dels = append(h.dels, o)
		case "reg":
			var o regOp
			dec(&o)
			h.regs = append(h.regs, o)
		case "d1":
			var o d1Op
			dec(&o)
			h.d1s = append(h.d1s, o)
		case "kv":
			var o kvOp
			dec(&o)
			h.kvs = append(h.kvs, o)
		case "pulse":
			var o pulseEv
			dec(&o)
			h.pulses = append(h.pulses, o)
		case "lock":
			var o lockEv
			dec(&o)
			h.locks = append(h.locks, o)
		case "fault":
			var o faultEv
			dec(&o)
			h.faults = append(h.faults, o)
		case "deployment":
			var o depEv
			dec(&o)
			h.deps = append(h.deps, o)
		}
	}
	for _, r := range []result{checkD1(h), checkReg(h), checkD2(h), checkD3(h), checkKV(h), checkD6(h)} {
		mark := "✓"
		if !r.Pass {
			mark = "✗"
			t.Fail()
		}
		t.Logf("%s %s: %s", mark, r.ID, r.Summary)
		for _, e := range r.Evidence {
			t.Logf("    %s", e)
		}
	}
}
