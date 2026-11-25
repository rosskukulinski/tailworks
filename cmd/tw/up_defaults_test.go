package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TW_STATE_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if got, want := defaultOAuthFile(), filepath.Join(home, ".config", "tailworks", "oauth.env"); got != want {
		t.Fatalf("defaultOAuthFile = %s, want %s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	if got, want := defaultOAuthFile(), filepath.Join(home, "xdg", "tailworks", "oauth.env"); got != want {
		t.Fatalf("defaultOAuthFile with XDG_CONFIG_HOME = %s, want %s", got, want)
	}

	def := filepath.Join(home, ".tailworks")
	if !isDefaultStateDir(def, "") {
		t.Fatal("~/.tailworks with no override is the default state dir")
	}
	if isDefaultStateDir(def, def) {
		t.Fatal("an explicit --state-dir is never the default (tests must not pick up the operator's credential)")
	}
	t.Setenv("TW_STATE_DIR", def)
	if isDefaultStateDir(def, "") {
		t.Fatal("$TW_STATE_DIR is never the default")
	}

	f := filepath.Join(home, "oauth.env")
	os.WriteFile(f, []byte("TS_OAUTH_CLIENT_SECRET=tskey-client-x-y\n"), 0o644)
	os.Chmod(f, 0o644)
	tightenSecretFile(f)
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", st.Mode().Perm())
	}
}
