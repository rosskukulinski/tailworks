//go:build e2edataplane

package dataplane

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestR2Replay re-runs the R2 checkers on a recorded run
// (TW_DP_REPLAY=<results dir of an r2-* run>). R5's blob count and R-conv
// need the live cluster and are left out.
func TestR2Replay(t *testing.T) {
	dir := os.Getenv("TW_DP_REPLAY")
	if dir == "" || !strings.Contains(filepath.Base(dir), "-r2-") {
		t.Skip("TW_DP_REPLAY not set to an r2 run")
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in := r2Input{backend: "s3", replicas: strings.Contains(dir, "-r2-replicas-"), blobErr: os.ErrNotExist}
	if strings.Contains(dir, "-r2-fs-") {
		in.backend = "fs"
	}
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
		switch line.Type {
		case "r2":
			var o r2Op
			json.Unmarshal(line.Ev, &o)
			in.ops = append(in.ops, o)
		case "fault":
			var o faultEv
			json.Unmarshal(line.Ev, &o)
			in.faults = append(in.faults, o)
		case "deployment":
			var o depEv
			json.Unmarshal(line.Ev, &o)
			in.deps = append(in.deps, o)
		}
	}
	for _, r := range checkR2All(in) {
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

// TestR2Checkers runs the R2 checkers on small made-up histories (no
// cluster), so a checker change can't silently stop catching things.
func TestR2Checkers(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	op := func(id, kind, key, val string, sent, recv float64, ok bool) r2Op {
		o := r2Op{ID: id, Op: kind, Key: key, Val: val, Sent: at(sent), Recv: at(recv), OK: ok, Host: "h1", DNS: "h1-r2.ts.net"}
		if kind == "put" {
			o.Etag = "etag-" + val
		}
		if !ok {
			o.Err = "500 boom"
		}
		return o
	}
	absent := func(o r2Op) r2Op { o.Absent = true; return o }
	with := func(o r2Op, f func(*r2Op)) r2Op { f(&o); return o }
	cut := []faultEv{{Kind: "cut-short", Host: "h1", Start: at(10), End: at(20)}}
	takeover := []faultEv{{Kind: "cut-takeover", Host: "h1", Start: at(10), End: at(80)}}
	pass := func(rs []result, id string) bool {
		for _, r := range rs {
			if r.ID == id {
				return r.Pass
			}
		}
		t.Fatalf("no result %s", id)
		return false
	}
	for _, tc := range []struct {
		name     string
		ops      []r2Op
		faults   []faultEv
		replicas bool
		fail     string // the result that must fail ("" none)
	}{
		{"linear", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("g1", "get", "k0", "a", 2, 3, true), op("d", "del", "k0", "", 4, 5, true), absent(op("g2", "get", "k0", "", 6, 7, true))}, nil, false, ""},
		{"stale", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("b", "put", "k0", "b", 2, 3, true), op("g", "get", "k0", "a", 4, 5, true)}, nil, false, "R1"},
		{"stale in an outage", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("b", "put", "k0", "b", 2, 3, true), op("g", "get", "k0", "a", 12, 13, true)}, cut, false, ""},
		{"stale past the fence bound", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("b", "put", "k0", "b", 2, 3, true), op("g", "get", "k0", "a", 55, 56, true)}, takeover, false, "R4"},
		{"no fence bound with replicas", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("b", "put", "k0", "b", 2, 3, true), op("g", "get", "k0", "a", 55, 56, true)}, takeover, true, ""},
		{"unknown outcome may land late", []r2Op{op("a", "put", "k0", "a", 0, 1, false), op("b", "put", "k0", "b", 2, 3, true), op("g", "get", "k0", "a", 4, 5, true)}, nil, false, ""},
		{"lost put", []r2Op{op("a", "put", "k0", "a", 0, 1, true), absent(op("g", "get", "k0", "", 4, 5, true))}, nil, false, "R1"},
		{"lost put at quiescence", []r2Op{op("a", "put", "k0", "a", 0, 1, true), with(absent(op("g", "get", "k0", "", 4, 5, true)), func(o *r2Op) { o.Final = true })}, nil, false, "R2"},
		{"older than an earlier read", []r2Op{op("a", "put", "k0", "a", 0, 1, true), op("b", "put", "k0", "b", 2, 9, false), op("g1", "get", "k0", "b", 3, 4, true), op("g2", "get", "k0", "a", 5, 6, true)}, nil, false, "R1"},
		{"read from the future", []r2Op{op("g", "get", "k0", "a", 0, 1, true), op("a", "put", "k0", "a", 2, 3, true)}, nil, false, "R1"},
		{"write acked while cut off", []r2Op{op("a", "put", "k0", "a", 11, 12, true)}, cut, false, "R3"},
		{"write acked past a move", []r2Op{with(op("a", "put", "k0", "a", 11, 12, true), func(o *r2Op) { o.Probe = true })}, nil, false, "R3"},
		{"wrong MD5 acked", []r2Op{with(op("a", "put", "k0", "a", 0, 1, true), func(o *r2Op) { o.BadMD5 = true })}, nil, false, "R2"},
		{"wrong bytes", []r2Op{op("a", "put", "k0", "a", 0, 1, true), with(op("g", "get", "k0", "a", 2, 3, true), func(o *r2Op) { o.Bad = "bytes" })}, nil, false, "R2"},
		{"blob missing on s3", []r2Op{op("a", "put", "k0", "a", 0, 1, true), with(op("g", "get", "k0", "", 2, 3, false), func(o *r2Op) { o.Err = "500 tw r2: r2_blob_missing: gone" })}, nil, false, "R2"},
		{"torn multipart", []r2Op{with(op("u", "get", "m0", "u", 2, 3, true), func(o *r2Op) { o.Bad = "torn" })}, nil, false, "R6"},
		{"upload lost", []r2Op{with(op("c", "mpu-create", "m0", "u", 0, 1, true), func(o *r2Op) { o.Upload = "U" }), with(op("u", "mpu-complete", "m0", "u", 2, 3, false), func(o *r2Op) { o.Upload, o.Err = "U", "500 The specified multipart upload does not exist." })}, nil, false, "R6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := r2Input{ops: tc.ops, faults: tc.faults, backend: "s3", replicas: tc.replicas}
			if tc.name == "write acked past a move" {
				in.deps = []depEv{{At: at(0), Host: "h1", Epoch: 1}, {At: at(5), Host: "h2", Epoch: 2}}
			}
			rs := checkR2All(in)
			for _, id := range []string{"R1", "R2", "R3", "R4", "R6"} {
				if want := id != tc.fail; pass(rs, id) != want {
					t.Errorf("%s pass=%v, want %v: %+v", id, !want, want, rs)
				}
			}
		})
	}
}
