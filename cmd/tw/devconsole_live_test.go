package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/agenteval"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevConsoleLive: a real `tw dev` run announces its console (--json
// `console` event), and the console shows the KV keys and D1 rows the app
// just wrote.
func TestDevConsoleLive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "con")
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.MkdirAll(filepath.Join(app, "migrations"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{ "name": "con", "tailnet": { "tags": ["tag:tw-wl-con"] } }`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "con", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "kv_namespaces": [{ "binding": "KV", "id": "cache" }],
  "d1_databases": [{ "binding": "DB", "database_name": "con-db", "database_id": "x" }]}`), 0o644)
	os.WriteFile(filepath.Join(app, "migrations", "0001_init.sql"), []byte("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT);\n"), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`export default { async fetch(req, env) {
  await env.KV.put("greeting", "hello from kv");
  await env.DB.prepare("INSERT INTO notes (body) VALUES (?)").bind("hello from d1").run();
  return new Response("ok");
} };`), 0o644)

	cmd := exec.Command(filepath.Join(bin, "tw"), "dev", app, "--port", "0", "--json", "--state-dir", filepath.Join(work, "state"))
	cmd.Env = os.Environ()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("not an NDJSON line: %q", sc.Text())
				continue
			}
			if err := agenteval.CheckDoc("dev.event", m); err != nil {
				t.Error(err)
			}
			lines <- m
		}
		close(lines)
	}()
	next := func(want string) map[string]any {
		t.Helper()
		deadline := time.After(90 * time.Second)
		for {
			select {
			case m, open := <-lines:
				if !open {
					t.Fatalf("tw dev exited before a %q event", want)
				}
				if m["event"] == want {
					return m
				}
			case <-deadline:
				t.Fatalf("no %q event", want)
			}
		}
	}
	base, _ := next("ready")["url"].(string)
	console, _ := next("console")["url"].(string)
	if !strings.HasPrefix(console, "http://127.0.0.1:") {
		t.Fatalf("console url %q", console)
	}
	if resp, err := http.Get(base + "/"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("app: %v %v", resp, err)
	}
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(strings.TrimSuffix(console, "/") + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d\n%s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	if b := get("/"); !strings.Contains(b, `href="/kv/cache"`) || !strings.Contains(b, `href="/d1/con-db"`) {
		t.Fatalf("console overview:\n%s", b)
	}
	if b := get("/kv/cache"); !strings.Contains(b, "greeting") || !strings.Contains(b, "hello from kv") {
		t.Fatalf("console KV:\n%s", b)
	}
	if b := get("/d1/con-db"); !strings.Contains(b, `href="/d1/con-db/notes"`) {
		t.Fatalf("console D1 tables:\n%s", b)
	}
	if b := get("/d1/con-db/notes"); !strings.Contains(b, "hello from d1") {
		t.Fatalf("console D1 rows:\n%s", b)
	}

	cmd.Process.Signal(syscall.SIGINT)
	next("stopping")
	cmd.Wait()
}
