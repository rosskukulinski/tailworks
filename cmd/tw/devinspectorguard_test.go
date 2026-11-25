package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRT_HDevInspector_OriginHostTarget pins issue #335 (ledger
// H-dev-inspector) through the proxy tw dev --inspector-port serves
// (devInspector): a web page can't attach (foreign Origin: 403), can't read
// /json through DNS rebinding (foreign Host: 421), and can't reach
// tailworks' own isolates (platform, deny: dropped from /json, 403 on
// their paths), while Chrome DevTools and VS Code still attach. On master
// every request was forwarded.
func TestRT_HDevInspector_OriginHostTarget(t *testing.T) {
	t.Parallel()
	// A stand-in for workerd's inspector: it lists three targets, as
	// workerd does for tailworks' config, and upgrades any of them.
	var inner string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json", "/json/list":
			var items []string
			for _, id := range []string{"platform", "tenant", "deny"} {
				items = append(items, `{"id":"`+id+`","title":"workerd: worker `+id+`","webSocketDebuggerUrl":"ws://`+inner+`/`+id+`","devtoolsFrontendUrl":"devtools://devtools/bundled/js_app.html?experiments=true&v8only=true&ws=`+inner+`/"}`)
			}
			io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case "/json/version":
			io.WriteString(w, `{"Browser": "workerd", "Protocol-Version": "1.3" }`)
		case "/platform", "/tenant", "/deny":
			c, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			rw.Flush()
			c.Close()
		default:
			http.Error(w, "Unknown worker session", http.StatusNotFound)
		}
	}))
	defer up.Close()
	inner = strings.TrimPrefix(up.URL, "http://")
	srv, outer, err := devInspector(0, func() string { return inner })
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	port := strings.TrimPrefix(outer, "127.0.0.1:")

	// do sends one request on a fresh connection with the given Host and
	// Origin ("" = no header), as a WebSocket upgrade when ws is set.
	do := func(path, host, origin string, ws bool) (int, string) {
		t.Helper()
		c, err := net.Dial("tcp", outer)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\n"
		if origin != "" {
			req += "Origin: " + origin + "\r\n"
		}
		if ws {
			req += "Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
		} else {
			req += "Connection: close\r\n"
		}
		io.WriteString(c, req+"\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s Host %q Origin %q: %v", path, host, origin, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusSwitchingProtocols {
			return resp.StatusCode, ""
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// A DevTools-style client still attaches to the app: no Origin
	// (chrome://inspect discovery, VS Code's js-debug), a debugger front
	// end's origin, or the proxy's own loopback origin; any loopback name.
	for _, tc := range []struct{ host, origin string }{
		{outer, ""},
		{"localhost:" + port, ""},
		{"[::1]:" + port, ""},
		{"LOCALHOST:" + port, ""},
		{outer, "devtools://devtools"},
		{outer, "chrome-devtools://devtools"},
		{outer, "vscode-webview://abc123"},
		{outer, "vscode-file://vscode-app"},
		{outer, "http://" + outer},
		{outer, "http://localhost:" + port},
	} {
		if code, body := do("/tenant", tc.host, tc.origin, true); code != http.StatusSwitchingProtocols {
			t.Errorf("debugger (Host %q, Origin %q) refused: %d %s", tc.host, tc.origin, code, body)
		}
	}
	if code, body := do("/json/list", outer, "", false); code != 200 || !strings.Contains(body, `"webSocketDebuggerUrl":"ws://`+outer+`/tenant"`) || !strings.Contains(body, "ws="+outer+"/tenant") {
		t.Errorf("/json/list for a debugger: %d %s", code, body)
	}
	if code, body := do("/json/version", outer, "", false); code != 200 || !strings.Contains(body, "workerd") {
		t.Errorf("/json/version: %d %s", code, body)
	}

	// A web page's upgrade carries its own Origin: 403, before workerd.
	for _, origin := range []string{
		"https://evil.example",
		"http://evil.example:" + port,
		"http://127.0.0.1:1",
		"null",
		"file://",
		"http://" + outer + "/x",
	} {
		if code, body := do("/tenant", outer, origin, true); code != http.StatusForbidden {
			t.Errorf("upgrade with Origin %q: %d %s, want 403", origin, code, body)
		}
	}
	// A rebound DNS name (or any other Host): 421, for /json and upgrades.
	for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1:1", "localhost", "127.0.0.2:" + port, ""} {
		if code, body := do("/json/list", host, "", false); code != http.StatusMisdirectedRequest {
			t.Errorf("/json/list with Host %q: %d %s, want 421", host, code, body)
		}
		if code, body := do("/tenant", host, "", true); code != http.StatusMisdirectedRequest {
			t.Errorf("upgrade with Host %q: %d %s, want 421", host, code, body)
		}
	}

	// tailworks' own isolates are not targets: not listed, not reachable.
	_, list := do("/json", outer, "", false)
	for _, id := range []string{"platform", "deny"} {
		if strings.Contains(list, id) {
			t.Errorf("/json lists %s: %s", id, list)
		}
		if code, body := do("/"+id, outer, "", true); code != http.StatusForbidden {
			t.Errorf("upgrade to /%s: %d %s, want 403", id, code, body)
		}
		if code, body := do("/"+id, outer, "devtools://devtools", true); code != http.StatusForbidden {
			t.Errorf("devtools upgrade to /%s: %d %s, want 403", id, code, body)
		}
	}
	for _, p := range []string{"/", "/tenant/", "/workflows", "/assets", "/%2e%2e/platform"} {
		if code, body := do(p, outer, "", true); code != http.StatusForbidden && code != http.StatusBadRequest {
			t.Errorf("upgrade to %s: %d %s, want 403", p, code, body)
		}
	}
}
