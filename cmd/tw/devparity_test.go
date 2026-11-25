package main

// Issue #42, tw dev parity with wrangler dev: source-mapped exception
// stacks with tailworks' runtime frames folded, req.url keeping the host the
// developer used, --inspector-port, hotkeys.

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/agenteval"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func TestRewriteMapSources(t *testing.T) {
	t.Parallel()
	app := filepath.FromSlash("/home/u/app")
	out := filepath.FromSlash("/tmp/tw-build-1")
	raw := `{"version":3,"sourceRoot":"","sources":["../../home/u/app/src/index.ts","../../home/u/app/node_modules/hono/dist/index.js","../../opt/tailworks/share/node_modules/@tailworks/tailnet/dist/caller.js","tw-stub:email"],"mappings":"AAAA"}`
	b, err := rewriteMapSources([]byte(raw), out, app)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Sources []string `json:"sources"`
	}
	json.Unmarshal(b, &m)
	want := []string{"src/index.ts", "node_modules/hono/dist/index.js", "node_modules/@tailworks/tailnet/dist/caller.js", "tw-stub:email"}
	if strings.Join(m.Sources, ",") != strings.Join(want, ",") {
		t.Fatalf("sources %v, want %v", m.Sources, want)
	}
	if strings.Contains(string(b), "sourceRoot") || !strings.Contains(string(b), `"mappings":"AAAA"`) {
		t.Fatalf("map: %s", b)
	}
}

func TestWantSourceMap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		mode        sourceMapMode
		upload      any
		on, content bool
	}{
		{sourceMapConfig, nil, true, false},
		{sourceMapConfig, true, true, false},
		{sourceMapConfig, false, false, false}, // wrangler's opt-out, for tw deploy
		{sourceMapDev, false, true, false},     // tw dev maps anyway
		{sourceMapInline, nil, true, true},
	} {
		a := &app{Wrangler: map[string]any{}, Opts: appOpts{SourceMap: c.mode}}
		if c.upload != nil {
			a.Wrangler["upload_source_maps"] = c.upload
		}
		on, content := a.wantSourceMap()
		if on != c.on || content != c.content {
			t.Errorf("mode %d upload %v: got %v %v, want %v %v", c.mode, c.upload, on, content, c.on, c.content)
		}
	}
	if devSourceMapMode(0) != sourceMapDev || devSourceMapMode(9229) != sourceMapInline {
		t.Fatal("devSourceMapMode")
	}
}

func TestDevHotkeys(t *testing.T) {
	t.Parallel()
	for c, want := range map[byte]devHotkeyAction{'b': hotkeyBrowser, 'B': hotkeyBrowser, 'x': hotkeyQuit, 'q': hotkeyQuit, 'Q': hotkeyQuit, 'd': hotkeyNone, '\n': hotkeyNone} {
		if got := devHotkey(c); got != want {
			t.Errorf("devHotkey(%q) = %d, want %d", c, got, want)
		}
	}
	if devHotkeysOn(true) {
		t.Fatal("hotkeys on under --json")
	}
	// go test's stdin is not a terminal.
	if devHotkeysOn(false) {
		t.Fatal("hotkeys on without a terminal")
	}
}

// The inspector proxy forwards to the active workerd and rewrites its /json
// answers to point at itself; with no workerd it answers 503.
func TestDevInspectorProxy(t *testing.T) {
	t.Parallel()
	var inner string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list", "/json":
			io.WriteString(w, `[{"id":"tenant","webSocketDebuggerUrl":"ws://`+inner+`/tenant","devtoolsFrontendUrl":"devtools://devtools/bundled/js_app.html?ws=`+inner+`/"}]`)
		default:
			io.WriteString(w, "other "+r.URL.Path)
		}
	}))
	defer up.Close()
	inner = strings.TrimPrefix(up.URL, "http://")
	target := ""
	srv, outer, err := devInspector(0, func() string { return target })
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	get := func(p string) (int, string) {
		resp, err := http.Get("http://" + outer + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/json/list"); code != 503 {
		t.Fatalf("no workerd: %d", code)
	}
	target = inner
	code, body := get("/json/list")
	if code != 200 || strings.Contains(body, inner) || !strings.Contains(body, "ws://"+outer+"/tenant") || !strings.Contains(body, "ws="+outer+"/") {
		t.Fatalf("/json/list: %d %s", code, body)
	}
	if code, body := get("/x"); code != http.StatusForbidden { // not a target (devinspectorguard.go)
		t.Fatalf("/x: %d %s", code, body)
	}
	if _, _, err := devInspector(mustPort(t, outer), func() string { return "" }); err == nil {
		t.Fatal("a second proxy on the same port")
	}
}

func mustPort(t *testing.T, addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDevParity runs `tw dev --json --inspector-port` on a template app
// that throws: the request record's stack points at src/…, req.url keeps
// 127.0.0.1:<port> while the caller stays the dev user, and the inspector
// lists the app's isolate.
func TestDevParity(t *testing.T) {
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
	app := filepath.Join(work, "hello")
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	e.MustJSON(nil, "init", app, "--template", "hello")
	os.WriteFile(filepath.Join(app, "src", "boom.ts"), []byte("// helper\nexport function explode(where: string): never {\n  throw new Error(\"boom at \" + where);\n}\n"), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.ts"), []byte(`import { getCaller } from "@tailworks/tailnet";
import { explode } from "./boom";

export default {
  async fetch(req: Request): Promise<Response> {
    const u = new URL(req.url);
    if (u.pathname === "/boom") explode(u.pathname);
    const who = getCaller(req);
    return Response.json({ url: req.url, user: who.user?.login ?? null });
  },
};
`), 0o644)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ip := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cmd := exec.Command(filepath.Join(bin, "tw"), "dev", app, "--port", "0", "--json", "--inspector-port", strconv.Itoa(ip))
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
	// next skips events until one of kind want that passes ok.
	next := func(want string, ok func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.After(90 * time.Second)
		for {
			select {
			case m, open := <-lines:
				if !open {
					t.Fatalf("tw dev exited before a %q event", want)
				}
				if m["event"] == want && (ok == nil || ok(m)) {
					return m
				}
			case <-deadline:
				t.Fatalf("no %q event", want)
			}
		}
	}
	ready := next("ready", nil)
	base, _ := ready["url"].(string)
	insp := next("inspector", nil)
	if insp["addr"] != "127.0.0.1:"+strconv.Itoa(ip) {
		t.Fatalf("inspector event: %v", insp)
	}

	// req.url keeps the host the developer used; the caller is still the
	// dev user (WhoIs of the proxy's node, not the Host header).
	resp, err := http.Get(base + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ URL, User string }
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.URL != base+"/hello" || got.User == "" {
		t.Fatalf("req.url %q (want %q), user %q", got.URL, base+"/hello", got.User)
	}

	// An uncaught exception: the record's stack is mapped to src/.
	if resp, err := http.Get(base + "/boom"); err != nil || resp.StatusCode != 500 {
		t.Fatalf("/boom: %v %v", resp, err)
	}
	rec := next("request", func(m map[string]any) bool {
		r, _ := m["record"].(map[string]any)
		return r["path"] == "/boom" && r["exceptions"] != nil
	})["record"].(map[string]any)
	stack := rec["exceptions"].([]any)[0].(map[string]any)["stack"].(string)
	if !strings.Contains(stack, "src/boom.ts:3:") || !strings.Contains(stack, "src/index.ts:7:") || strings.Contains(stack, "dist/index.js") {
		t.Fatalf("stack not mapped:\n%s", stack)
	}

	// The inspector lists the app's isolate, behind the fixed port.
	resp, err = http.Get("http://127.0.0.1:" + strconv.Itoa(ip) + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "ws://127.0.0.1:"+strconv.Itoa(ip)+"/tenant") || strings.Contains(string(b), "/platform") {
		t.Fatalf("/json/list (only the tenant target, issue #335): %s", b)
	}

	cmd.Process.Signal(syscall.SIGINT)
	next("stopping", nil)
	cmd.Wait()
}
