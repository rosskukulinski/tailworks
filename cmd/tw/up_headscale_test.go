package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpHeadscaleInference: --api-key-file selects the Headscale profile only
// with a non-default --control-url (#539); with Tailscale's control URL, or
// an explicit --control=tailscale, it stays mint_mode_removed (ADR-042).
func TestUpHeadscaleInference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TW_STATE_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	key := filepath.Join(home, "hs.env")
	if err := os.WriteFile(key, []byte("HEADSCALE_API_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hsURL := "http://127.0.0.1:1"
	code := func(args ...string) (string, string) {
		t.Helper()
		err := cmdUp(append([]string{"--state-dir", filepath.Join(home, "st"), "--yes"}, args...))
		var ce *cliErr
		if !errors.As(err, &ce) {
			t.Fatalf("tw up %v: want a cli error, got %v", args, err)
		}
		return ce.Code, strings.Join(append(append([]string{ce.Message}, ce.Lines...), ce.Fix...), "\n")
	}
	// Inferred: the profile's own checks run (--tailnet is required).
	if c, txt := code("--api-key-file", key, "--control-url", hsURL); c != "tailnet_required" {
		t.Fatalf("--api-key-file + Headscale --control-url: %s\n%s", c, txt)
	}
	// Explicit --control=headscale is still accepted.
	if c, txt := code("--control=headscale", "--api-key-file", key, "--control-url", hsURL); c != "tailnet_required" {
		t.Fatalf("--control=headscale: %s\n%s", c, txt)
	}
	// Tailscale's control URL: the removed API-key mint mode, with the hint.
	if c, txt := code("--api-key-file", key); c != "mint_mode_removed" || !strings.Contains(txt, headscaleOnHint) {
		t.Fatalf("--api-key-file on Tailscale: %s\n%s", c, txt)
	}
	// An explicit --control=tailscale always wins.
	if c, txt := code("--control=tailscale", "--api-key-file", key, "--control-url", hsURL); c != "mint_mode_removed" {
		t.Fatalf("--control=tailscale --api-key-file: %s\n%s", c, txt)
	}
	// --control=headscale without a key file says so (not the OAuth text).
	if c, txt := code("--control=headscale", "--control-url", hsURL, "--tailnet", "hs.example"); c != "bad_request" || !strings.Contains(txt, "--api-key-file") {
		t.Fatalf("--control=headscale without a key: %s\n%s", c, txt)
	}
}

// TestUpHeadscaleDefaultKey: on the default state dir, a Headscale
// --control-url picks up ~/.config/tailworks/headscale.env; a bare tw up
// (Tailscale's control URL) never looks there, and other state dirs never do.
func TestUpHeadscaleDefaultKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TW_STATE_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	want := filepath.Join(home, ".config", "tailworks", "headscale.env")
	if got := defaultHeadscaleFile(); got != want {
		t.Fatalf("defaultHeadscaleFile = %s, want %s", got, want)
	}
	f := newFlags("up")
	profile := f.String("control", controlTailscale, "")
	if err := f.FlagSet.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if p := headscaleDefaultKey(f.FlagSet, *profile, "https://hs.example"); p != "" {
		t.Fatalf("no file yet, got %s", p)
	}
	os.MkdirAll(filepath.Dir(want), 0o700)
	if err := os.WriteFile(want, []byte("HEADSCALE_API_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := headscaleDefaultKey(f.FlagSet, *profile, "https://hs.example"); p != want {
		t.Fatalf("Headscale control URL: got %q, want %s", p, want)
	}
	if p := headscaleDefaultKey(f.FlagSet, *profile, prodControl); p != "" {
		t.Fatalf("Tailscale's control URL must keep the OAuth path, got %s", p)
	}
	ft := newFlags("up")
	pt := ft.String("control", controlTailscale, "")
	if err := ft.FlagSet.Parse([]string{"--control=tailscale"}); err != nil {
		t.Fatal(err)
	}
	if p := headscaleDefaultKey(ft.FlagSet, *pt, "https://hs.example"); p != "" {
		t.Fatalf("explicit --control=tailscale, got %s", p)
	}

	// End to end through cmdUp on the default state dir: the key is found,
	// so the profile's --tailnet check is what stops it.
	err := cmdUp([]string{"--yes", "--control-url", "http://127.0.0.1:1"})
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "tailnet_required" {
		t.Fatalf("tw up --control-url <hs> with headscale.env: %v", err)
	}
}
