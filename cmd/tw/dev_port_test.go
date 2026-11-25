package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevPortValue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		dev  any
		port int
		set  bool
	}{
		{nil, 0, false},
		{map[string]any{"ip": "0.0.0.0"}, 0, false},
		{map[string]any{"port": float64(9000)}, 9000, true},
		{map[string]any{"port": "9000"}, 0, true},
		{map[string]any{"port": float64(0)}, 0, true},
		{map[string]any{"port": float64(70000)}, 0, true},
		{map[string]any{"port": 1.5}, 0, true},
	} {
		if p, set := devPortValue(c.dev); p != c.port || set != c.set {
			t.Errorf("devPortValue(%v) = %d, %v; want %d, %v", c.dev, p, set, c.port, c.set)
		}
	}
}

func TestDevWranglerNote(t *testing.T) {
	t.Parallel()
	if n := devWranglerNote(map[string]any{"port": float64(9000)}); n != "" {
		t.Errorf("dev.port alone: %q, want no warning", n)
	}
	n := devWranglerNote(map[string]any{"port": float64(9000), "ip": "0.0.0.0", "local_protocol": "https"})
	if !strings.Contains(n, "dev.ip, dev.local_protocol ignored") || strings.Contains(n, "dev.port ") {
		t.Errorf("ip + local_protocol: %q", n)
	}
	if n := devWranglerNote(map[string]any{"port": "x"}); !strings.Contains(n, "not a port number") {
		t.Errorf("bad port: %q", n)
	}
	// Through wranglerWarnings, as tw deploy and tw dev print them.
	w := wranglerWarnings("wrangler.jsonc", map[string]any{"dev": map[string]any{"port": float64(9000)}}, "esbuild")
	if len(w) != 0 {
		t.Errorf("wranglerWarnings: %q", w)
	}
}

// devBusyPort holds a 127.0.0.1 port for the test's lifetime.
func devBusyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestResolveDevPort(t *testing.T) {
	t.Parallel()
	busy := devBusyPort(t)
	noCfg := &app{Dir: t.TempDir()}

	// --port given and busy: an explicit choice fails.
	if _, err := resolveDevPort(busy, true, noCfg, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("--port busy: %v", err)
	} else if ce := asCLIErr(err); !strings.Contains(strings.Join(ce.Fix, " "), "--port 0") {
		t.Fatalf("fix lacks --port 0: %q", ce.Fix)
	}

	// Nothing chose a port and the default is busy: a free one, one line.
	var w bytes.Buffer
	p, err := resolveDevPort(busy, false, noCfg, &w)
	if err != nil || p == busy || p == 0 {
		t.Fatalf("fallback: %d, %v", p, err)
	}
	if !strings.Contains(w.String(), fmt.Sprintf("port %d is in use", busy)) || !strings.Contains(w.String(), fmt.Sprintf("uses %d", p)) {
		t.Fatalf("fallback line: %q", w.String())
	}

	// wrangler dev.port busy: an explicit choice fails, naming the file.
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{"name": "x", "main": "index.js", "dev": {"port": %d}}`, busy)
	if err := os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	withCfg := &app{Dir: dir, WranglerFile: "wrangler.jsonc"}
	if _, err := resolveDevPort(devDefaultPort, false, withCfg, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "dev.port in wrangler.jsonc") {
		t.Fatalf("dev.port busy: %v", err)
	}
	// --port wins over dev.port.
	if p, err := resolveDevPort(0, true, withCfg, &bytes.Buffer{}); err != nil || p != 0 {
		t.Fatalf("--port 0 with dev.port: %d, %v", p, err)
	}
	if got := devWranglerPort(withCfg); got != busy {
		t.Fatalf("devWranglerPort = %d, want %d", got, busy)
	}
}
