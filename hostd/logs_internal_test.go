package hostd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logsHost is a Host with tenant tnA's workload "web" active in dir, and
// tenant tnB's workload "other".
func logsHost(t *testing.T) (*Host, string) {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.MkdirAll(a, 0o700)
	os.MkdirAll(b, 0o700)
	os.WriteFile(filepath.Join(b, "workerd.log"), []byte("tenant B secret\n"), 0o600)
	wa := &workload{tenantID: "tnA", name: "web"}
	wa.active = &instance{w: wa, dir: a}
	wb := &workload{tenantID: "tnB", name: "other"}
	wb.active = &instance{w: wb, dir: b}
	h := &Host{cfg: Config{HostID: "h1"}, workloads: map[string]*workload{"tnA/web": wa, "tnB/other": wb}}
	return h, filepath.Join(a, "workerd.log")
}

// The log is resolved from the host's record of (tenant, workload) only:
// another tenant's workload, traversal and key-confusing names, and
// workloads that aren't running are refused.
func TestWorkloadLogFileResolution(t *testing.T) {
	t.Parallel()
	h, want := logsHost(t)
	if p, err := h.WorkloadLogFile("tnA", "web"); err != nil || p != want {
		t.Fatalf("own workload: %q %v, want %q", p, err, want)
	}
	// "x/y" + "z" and "x" + "y/z" would both be key "x/y/z".
	h.workloads["tnA/web/x"] = h.workloads["tnA/web"]
	for _, tc := range [][2]string{
		{"tnA", "other"},        // tenant A asking for tenant B's workload
		{"tnB", "web"},          // and the other way round
		{"tnA/web", "x"},        // key confusion through the tenant
		{"tnA", "web/x"},        // and through the workload
		{"..", "web"},           // traversal
		{"tnA", ".."},           // traversal
		{"tnA", "../b"},         // traversal
		{"tnA", "web/../../b"},  // traversal
		{"", "web"},             // missing
		{"tnA", ""},             // missing
		{"tnA", "nope"},         // not deployed
		{"tnA", "Web"},          // not a DNS label
		{"tnA", "web%2f..%2fb"}, // encoded
	} {
		if p, err := h.WorkloadLogFile(tc[0], tc[1]); err == nil {
			t.Errorf("WorkloadLogFile(%q, %q) = %q, want refused", tc[0], tc[1], p)
		}
	}
	h.workloads["tnA/idle"] = &workload{tenantID: "tnA", name: "idle"} // no active instance
	if _, err := h.WorkloadLogFile("tnA", "idle"); err == nil {
		t.Error("a workload without an active instance has no log")
	}
	nodeDir := t.TempDir()
	h.workloads["tnA/web"].active.node = &nodeProc{logPath: filepath.Join(nodeDir, "app.log")}
	if p, _ := h.WorkloadLogFile("tnA", "web"); p != filepath.Join(nodeDir, "app.log") {
		t.Errorf("node tier log = %q", p)
	}
}

func TestLogsHandlerTailAndRefusals(t *testing.T) {
	t.Parallel()
	h, path := logsHost(t)
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	os.WriteFile(path, []byte(sb.String()), 0o600)
	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.logsHandler(w, httptest.NewRequest("GET", "/v1/logs?"+q, nil))
		return w
	}
	w := get("tenant=tnA&workload=web&n=3")
	if w.Code != 200 || w.Body.String() != "line 98\nline 99\nline 100\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("tail: %d %q", w.Code, w.Body.String())
	}
	if w := get("tenant=tnA&workload=web"); strings.Count(w.Body.String(), "\n") != 50 || !strings.HasPrefix(w.Body.String(), "line 51\n") {
		t.Fatalf("default n: %q", w.Body.String())
	}
	if w := get("tenant=tnA&workload=web&n=1000"); !strings.HasPrefix(w.Body.String(), "line 1\n") {
		t.Fatalf("n > lines: %q", w.Body.String())
	}
	if w := get("tenant=tnA&workload=web&n=0"); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("n=0: %d %q", w.Code, w.Body.String())
	}
	for q, code := range map[string]string{
		"tenant=tnA&workload=web&n=10001": "bad_request",
		"tenant=tnA&workload=web&n=-1":    "bad_request",
		"tenant=tnA&workload=other":       "unknown_workload",
		"tenant=tnA&workload=..%2Fb":      "unknown_workload",
		"tenant=..&workload=other":        "unknown_workload",
	} {
		w := get(q)
		var e struct{ Code string }
		json.Unmarshal(w.Body.Bytes(), &e)
		if e.Code != code || strings.Contains(w.Body.String(), "secret") {
			t.Errorf("%s: %d %s, want %s", q, w.Code, w.Body.String(), code)
		}
	}
	os.Remove(path)
	if w := get("tenant=tnA&workload=web"); w.Code != 404 || !strings.Contains(w.Body.String(), "no_logs") {
		t.Fatalf("missing file: %d %s", w.Code, w.Body.String())
	}
}

func TestTailLinesLongAndPartial(t *testing.T) {
	t.Parallel()
	f, _ := os.CreateTemp(t.TempDir(), "log")
	defer f.Close()
	long := strings.Repeat("x", 200<<10)   // several read chunks
	f.WriteString("a\n" + long + "\nb\nc") // no final newline
	out, end, err := tailLines(f, 3, maxLogTailBytes)
	if err != nil || string(out) != long+"\nb\nc\n" || end != int64(2+len(long)+1+3) {
		t.Fatalf("tail: %d bytes, end %d, %v", len(out), end, err)
	}
	if out, _, _ := tailLines(f, 2, 1024); string(out) != "b\nc\n" {
		t.Fatalf("bounded tail: %q", out)
	}
}

// follow=1 streams appended lines and a redeploy's new log file.
func TestLogsHandlerFollow(t *testing.T) {
	t.Parallel()
	h, path := logsHost(t)
	os.WriteFile(path, []byte("old\n"), 0o600)
	srv := httptest.NewServer(http.HandlerFunc(h.logsHandler))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/logs?tenant=tnA&workload=web&n=1&follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	next := func(want string) {
		t.Helper()
		l, err := rd.ReadString('\n')
		if err != nil || l != want+"\n" {
			t.Fatalf("got %q %v, want %q", l, err, want)
		}
	}
	next("old")
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	fh.WriteString("new 1\n")
	next("new 1")
	fh.WriteString("new 2\n")
	fh.Close()
	next("new 2")
	// A redeploy: a new instance dir with its own log.
	nd := filepath.Join(filepath.Dir(filepath.Dir(path)), "a2")
	os.MkdirAll(nd, 0o700)
	os.WriteFile(filepath.Join(nd, "workerd.log"), []byte("redeployed\n"), 0o600)
	w := h.workloads["tnA/web"]
	w.mu.Lock()
	w.active = &instance{w: w, dir: nd}
	w.mu.Unlock()
	next("redeployed")
}
