//go:build e2edataplane

package dataplane

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReplayWorkflows re-runs W1–W5 on a recorded run of
// TestDataplaneWorkflows (TW_DP_REPLAY=<results dir>), as TestReplay does
// for TestDataplane. W5 uses the recorded final statuses.
func TestReplayWorkflows(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" || !strings.Contains(filepath.Base(dir), "-workflows") {
		t.Skip("TW_DP_REPLAY not set to a workflows run")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := &history{}
	wh := &wfHist{h: h, nodeIDs: map[string]string{}}
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
		case "fault":
			var o faultEv
			dec(&o)
			h.faults = append(h.faults, o)
		case "deployment":
			var o depEv
			dec(&o)
			h.deps = append(h.deps, o)
		case "wf-create":
			var o wfCreate
			dec(&o)
			wh.creates = append(wh.creates, o)
		case "wf-do":
			var o wfDo
			dec(&o)
			wh.dos = append(wh.dos, o)
		case "wf-note":
			var o wfNote
			dec(&o)
			wh.notes = append(wh.notes, o)
		case "wf-event":
			var o wfEvent
			dec(&o)
			wh.events = append(wh.events, o)
		case "wf-decision":
			var o wfDecision
			dec(&o)
			wh.decs = append(wh.decs, o)
		case "wf-final":
			var o wfFinal
			dec(&o)
			wh.finals = append(wh.finals, o)
		case "wf-final-host":
			dec(&wh.finalDep)
		case "wf-audit":
			var o wfAudit
			dec(&o)
			wh.audits = append(wh.audits, o)
		case "wf-listed":
			var o wfListed
			dec(&o)
			wh.listed = append(wh.listed, o)
		}
	}
	w5 := result{ID: "W5", Pass: true, Summary: "replayed"}
	for _, r := range []result{checkW1(wh), checkW2(wh, h.faults, h.deps), checkW3(wh, h.faults, nil), checkW4(wh, h.deps, h.faults), checkW5(wh, w5)} {
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
