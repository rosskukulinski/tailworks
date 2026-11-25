package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWranglerEnvPutsBinOnPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	path := func(env []string) string {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok {
				return v
			}
		}
		return ""
	}
	bin := filepath.Join("/p", "node_modules", ".bin", "wrangler")
	if got, want := path(wranglerEnv(bin)), filepath.Join("/p", "node_modules", ".bin")+string(os.PathListSeparator)+"/usr/bin"; got != want {
		t.Errorf("PATH %q, want %q", got, want)
	}
	if got := path(wranglerEnv("/usr/local/bin/wrangler")); got != "/usr/bin" {
		t.Errorf("a wrangler on PATH changed PATH to %q", got)
	}
}
