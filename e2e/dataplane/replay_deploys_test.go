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

// TestReplayDeploys re-runs V1–V5 on a recorded deploys run
// (TW_DP_REPLAY=<results dir of a deploys-* scenario>). The final read,
// the value scan and V6 need the live cluster and are left out.
func TestReplayDeploys(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" || !strings.Contains(filepath.Base(dir), "-deploys") {
		t.Skip("TW_DP_REPLAY not set to a deploys run")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := &deploysRun{dh: &dhist{h: &history{}}, labelOf: map[string]string{}, vals: map[string]secretVal{}, recs: map[string]depRec{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
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
		case "bundle":
			var b struct{ Label, Sha string }
			dec(&b)
			d.labelOf[b.Sha] = b.Label
			d.bundles = append(d.bundles, dBundle{label: b.Label, sha: b.Sha})
		case "secval":
			var v struct {
				Digest, Name string
				Ver          int
			}
			dec(&v)
			d.vals[v.Digest] = secretVal{name: v.Name, ver: v.Ver}
		case "depop":
			var o depOp
			dec(&o)
			d.dh.ops = append(d.dh.ops, o)
		case "secop":
			var o secOp
			dec(&o)
			d.dh.secs = append(d.dh.secs, o)
		case "info":
			var o infoObs
			dec(&o)
			d.dh.obs = append(d.dh.obs, o)
		case "deprec":
			var e recEv
			dec(&e)
			d.dh.recs = append(d.dh.recs, e)
			d.recs[e.Rec.DeploymentID] = e.Rec
		case "audit":
			dec(&d.dh.audit)
		case "fault":
			var o faultEv
			dec(&o)
			d.dh.h.faults = append(d.dh.h.faults, o)
		}
	}
	for _, r := range []result{checkV1(d), checkV2(d), checkV3(d), checkV4(d, infoObs{Err: "not replayed"}, nil), checkV5(d, nil)} {
		mark := "✓"
		if !r.Pass {
			mark = "✗"
			t.Fail()
		}
		t.Logf("%s %s: %s", mark, r.ID, r.Summary)
		for _, e := range r.Evidence {
			t.Logf("    %s", e)
		}
		for _, e := range r.Notes {
			t.Logf("    (note) %s", e)
		}
	}
}
