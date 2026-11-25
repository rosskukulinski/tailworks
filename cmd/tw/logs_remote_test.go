package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rosskukulinski/tailworks/internal/features"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// `tw logs <workload>` on a linked machine: the CLI asks the linked mgmt
// (a real tw-mgmt in process behind its WhoIs gate, the caller an admin
// by the cap), which reads the log from the lease holder's hostd (a fake
// push listener).
func TestRemoteLogs(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "twlogs-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx := context.Background()
	st, err := mgmt.OpenStore(ctx, filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cust, err := mgmt.OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "api.env")
	writeFakeOAuthFile(t, keyFile)
	m, err := mgmt.New(mgmt.Config{Store: st, Custody: cust, Broker: &mgmt.Broker{BaseURL: "http://tsapi.invalid"},
		IDSecret: bytes.Repeat([]byte{7}, 32), ReapInterval: time.Hour, PolicyMaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	for _, tn := range []mgmt.TenantRequest{
		{ID: "tn_a", Tailnet: "a.ts.net", MintMode: mgmt.MintOAuth, OAuthClientFile: keyFile},
		{ID: "tn_b", Tailnet: "b.ts.net", MintMode: mgmt.MintOAuth, OAuthClientFile: keyFile},
	} {
		if _, err := m.CreateTenant(ctx, tn); err != nil {
			t.Fatal(err)
		}
	}

	// The host: hostd's GET /v1/logs, following until the request ends.
	var mu sync.Mutex
	var queries []string
	sock := filepath.Join(dir, "hostd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hsrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if q.Get("source") == "requests" {
			// Structured records (ADR-045), filtered by hostd.
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprintf(w, `{"time":"2026-11-15T10:00:00.123Z","kind":"request","workload":%q,"method":"GET","path":"/a","status":500,"durationMs":12.5,"outcome":"exception","class":"Counter","name":"alice","caller":{"user":"ross@example.com"},"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","logs":[{"level":"error","message":"boom"}]}`+"\n", q.Get("workload"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s/%s line 1\n%s/%s line 2\n", q.Get("tenant"), q.Get("workload"), q.Get("tenant"), q.Get("workload"))
		if q.Get("follow") == "1" {
			w.(http.Flusher).Flush()
			fmt.Fprintf(w, "followed n=%s\n", q.Get("n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	})}
	go hsrv.Serve(ln)
	t.Cleanup(func() { hsrv.Close() })
	if _, _, err := m.RegisterHost(ctx, mgmt.RegisterRequest{HostID: "h1", CapacityNodes: 10, Tiers: []string{"process"}, Features: features.Host(), PushURL: "unix://" + sock}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcquireLease(ctx, mgmt.LeaseName("tn_a", "web"), "h1", time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}

	// mgmt behind its WhoIs gate; this machine is an admin by the cap.
	srv := &mgmt.Server{M: m}
	api := httptest.NewServer(srv.TailnetHandler(func(context.Context, string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nDEV", Login: "dev@example.com", Caps: []string{mgmt.AdminCap}}, nil
	}, srv.Handler()))
	t.Cleanup(api.Close)
	devDir := filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o700)
	os.WriteFile(filepath.Join(devDir, "link.json"), []byte(`{"endpoint":"`+api.URL+`","linkedAt":"2026-11-14T00:00:00Z"}`), 0o600)

	run := func(args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		err := cmdLogs(append([]string{"--state-dir", devDir}, args...))
		w.Close()
		os.Stdout = old
		return <-done, err
	}

	out, err := run("web", "--console", "-n", "7")
	if err != nil || out != "tn_a/web line 1\ntn_a/web line 2\n" {
		t.Fatalf("remote logs: %q %v", out, err)
	}
	mu.Lock()
	if len(queries) != 1 || !strings.Contains(queries[0], "n=7") || strings.Contains(queries[0], "follow") || strings.Contains(queries[0], "source") {
		t.Fatalf("host query %v", queries)
	}
	mu.Unlock()
	// The default: request records, rendered; the filters reach the host.
	out, err = run("web", "--errors", "--status", "5xx", "--object", "Counter/alice", "-n", "3")
	want := "2026-11-15T10:00:00.123Z GET /a 500 12ms exception  Counter/alice  caller=ross@example.com  trace=4bf92f3577b34da6a3ce929d0e0e4736\n    error boom\n"
	if err != nil || out != want {
		t.Fatalf("records: %q %v", out, err)
	}
	mu.Lock()
	if q := queries[len(queries)-1]; !strings.Contains(q, "source=requests") || !strings.Contains(q, "errors=1") || !strings.Contains(q, "status=5xx") ||
		!strings.Contains(q, "object=Counter%2Falice") || !strings.Contains(q, "n=3") {
		t.Fatalf("host query %s", q)
	}
	queries = nil
	mu.Unlock()
	out, err = run("web", "--json")
	if err != nil || !strings.HasPrefix(out, `{"time":"2026-11-15T10:00:00.123Z","kind":"request"`) || strings.Count(out, "\n") != 1 {
		t.Fatalf("--json: %q %v", out, err)
	}

	// Refusals, each with its code.
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"--component", "mgmt"}, "remote_unsupported"},
		{[]string{"web", "--component", "hostd"}, "remote_unsupported"},
		{nil, "bad_request"}, // no app here and no deployed workload: never a daemon's log
		{[]string{"web", "-n", "10001"}, "bad_request"},
		{[]string{"web", "--console", "--errors"}, "bad_request"},
		{[]string{"web", "--status", "6xx"}, "bad_request"},
		{[]string{"nope"}, "unknown_workload"},
		{[]string{"web", "--tenant", "b.ts.net"}, "unknown_workload"}, // another tenant's path
		{[]string{"web", "--host", "h9"}, "unknown_workload"},
	} {
		if _, err := run(tc.args...); cliCode(err) != tc.code {
			t.Errorf("logs %v: %v, want %s", tc.args, err, tc.code)
		}
	}
	if _, err := run("--component", "supervisor"); !strings.Contains(cliText(err), "on the host") {
		t.Errorf("--component remotely should say where it works: %s", cliText(err))
	}

	// -f streams until interrupted.
	fctx, cancel := context.WithCancel(context.Background())
	old := remoteLogsContext
	remoteLogsContext = func() (context.Context, context.CancelFunc) { return fctx, cancel }
	t.Cleanup(func() { remoteLogsContext = old })
	go func() {
		for {
			mu.Lock()
			n := len(queries)
			mu.Unlock()
			if n >= 2 {
				time.Sleep(200 * time.Millisecond) // let the followed line through
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	out, err = run("web", "--console", "-f", "-n", "2")
	if err != nil || out != "tn_a/web line 1\ntn_a/web line 2\nfollowed n=2\n" {
		t.Fatalf("remote follow: %q %v", out, err)
	}

	// --console --json: one {"event": "log", "workload", "line"} per line
	// (docs/cli-json.md); records are already JSON Lines.
	remoteLogsContext = old
	out, err = run("web", "--console", "-n", "2", "--json")
	jsonOut = false
	var evs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			t.Fatalf("logs --json: not NDJSON: %q", out)
		}
		evs = append(evs, m)
	}
	if err != nil || len(evs) != 2 || evs[0]["event"] != "log" || evs[0]["workload"] != "web" || evs[1]["line"] != "tn_a/web line 2" || evs[0]["host"] != nil {
		t.Fatalf("remote logs --json: %q %v", out, err)
	}
}
