package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"github.com/rosskukulinski/tailworks/schema"
)

func TestSemver(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"0.2.0", "0.1.0", true},
		{"v0.3.0", "0.2.0-dev", true},
		{"0.2.0", "0.2.0-1a2b3c4d-dirty", false}, // suffixes are ignored (internal/version)
		{"0.2.0-dev", "0.2.0", false},
		{"0.2.0", "0.2.0", false},
		{"0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.0", true},
		{"0.1.1", "0.2.0", false},
	} {
		a, b := parseSemver(c.a), parseSemver(c.b)
		if !a.ok || !b.ok || a.newerThan(b) != c.newer {
			t.Errorf("%s newer than %s: got %v, want %v", c.a, c.b, a.newerThan(b), c.newer)
		}
	}
	if parseSemver("dev").ok || parseSemver("1.2").ok {
		t.Error("dev and 1.2 parsed as versions")
	}
}

// TestVersionCheck: `tw version --check` against a fake GitHub API: a
// newer release prints how to upgrade; --json matches $defs/version.
func TestVersionCheck(t *testing.T) {
	latest := "v0.3.0"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/rosskukulinski/tailworks/releases/latest" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"tag_name": latest, "html_url": "https://github.com/rosskukulinski/tailworks/releases/tag/" + latest})
	}))
	defer gh.Close()
	t.Setenv("TW_RELEASE_API", gh.URL)
	t.Setenv("TW_REPO", "")
	old := version
	t.Cleanup(func() { version = old })
	state := t.TempDir()

	version = "0.2.0"
	out, err := captureStdout(t, func() error { return cmdVersion([]string{"--check", "--state-dir", state}) })
	if err != nil || !strings.Contains(out, "tw 0.3.0 is out (this is 0.2.0)") || !strings.Contains(out, "upgrade: "+upgradeCommand("0.3.0")) {
		t.Fatalf("version --check: %v %q", err, out)
	}
	out, err = captureStdout(t, func() error { return cmdVersion([]string{"--check", "--json", "--state-dir", state}) })
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal([]byte(out), &v)
	if probs, err := schema.CheckCLI("version", v); err != nil || len(probs) > 0 {
		t.Fatalf("version --check --json vs schema: %v %v\n%s", probs, err, out)
	}
	if v["latest"] != "0.3.0" || v["updateAvailable"] != true || v["daemons"] != nil {
		t.Fatalf("version --check --json: %s", out)
	}
	// The check refreshed the notice's cache.
	if c := loadUpdateCache(state); c.Latest != "0.3.0" || time.Since(c.CheckedAt) > time.Minute {
		t.Fatalf("cache: %+v", c)
	}

	version = "0.3.0"
	out, _ = captureStdout(t, func() error { return cmdVersion([]string{"--check", "--state-dir", state}) })
	if !strings.Contains(out, "is the latest release") {
		t.Fatalf("up to date: %q", out)
	}
	version = "0.3.0-dev"
	out, _ = captureStdout(t, func() error { return cmdVersion([]string{"--check", "--state-dir", state}) })
	if !strings.Contains(out, "development build") {
		t.Fatalf("dev build: %q", out)
	}

	latest = ""
	if err := cmdVersion([]string{"--check", "--state-dir", state}); err == nil || !strings.Contains(err.Error(), "update_check_failed") {
		t.Fatalf("no release: %v", err)
	}
}

// TestUpdateNotice: the notice is printed at most once a day, only when a
// newer release is out, and never for --json, CI or a non-terminal.
func TestUpdateNotice(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "0.2.0"
	now := time.Now()
	c := updateCache{CheckedAt: now, Latest: "0.3.0"}
	if l := updateNoticeLine(c, now); !strings.Contains(l, "tw 0.3.0 is out") || !strings.Contains(l, "TW_NO_UPDATE_CHECK=1") {
		t.Fatalf("notice: %q", l)
	}
	c.NotifiedAt = now.Add(-time.Hour)
	if l := updateNoticeLine(c, now); l != "" {
		t.Fatalf("notice twice in a day: %q", l)
	}
	c.NotifiedAt, c.Latest = time.Time{}, "0.2.0"
	if l := updateNoticeLine(c, now); l != "" {
		t.Fatalf("notice without a newer release: %q", l)
	}
	var buf strings.Builder
	if updateNoticeEnabled("ls", &buf) {
		t.Fatal("notice enabled for a non-terminal writer")
	}
	t.Setenv("CI", "1")
	if updateNoticeEnabled("ls", nil) {
		t.Fatal("notice enabled under CI")
	}
}

// TestNoUpdateCheckAfterLocalCommands (#104): tw init and tw dev make no
// outbound request, even as an interactive release build whose daily check
// is due. The check is pointed at a counting server via TW_RELEASE_API; a
// command that does run it (ls) is the control.
func TestNoUpdateCheckAfterLocalCommands(t *testing.T) {
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.3.0"})
	}))
	defer gh.Close()
	t.Setenv("TW_RELEASE_API", gh.URL)
	t.Setenv("TW_REPO", "")
	t.Setenv("CI", "")
	t.Setenv("TW_NO_UPDATE_CHECK", "")
	state := t.TempDir()
	t.Setenv("TW_STATE_DIR", state)
	oldV, oldTerm, oldJSON := version, stderrIsTerminal, jsonOut
	t.Cleanup(func() { version, stderrIsTerminal, jsonOut = oldV, oldTerm, oldJSON })
	version = "0.2.0"
	stderrIsTerminal = func(io.Writer) bool { return true }

	// The whole commands through run(), as main calls them.
	app := filepath.Join(t.TempDir(), "hello")
	var stdout, stderr strings.Builder
	if code := run([]string{"init", app, "--template", "hello"}, &stdout, &stderr); code != 0 {
		t.Fatalf("tw init: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if _, err := nettest.WorkerdBinary(); err == nil && hostdtest.HasNode() {
		if code := run([]string{"dev", app, "--once", "--port", "0"}, &stdout, &stderr); code != 0 {
			t.Fatalf("tw dev --once: exit %d: %s%s", code, stdout.String(), stderr.String())
		}
	} else {
		t.Log("no workerd/node (make deps): tw dev --once not run, only its update-check gate")
	}
	jsonOut = false
	for _, cmd := range []string{"init", "dev"} {
		if updateNoticeEnabled(cmd, nil) {
			t.Fatalf("update check enabled after tw %s", cmd)
		}
		updateNotice(cmd, &stderr)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("tw init / tw dev made %d update-check request(s)", n)
	}
	if _, err := os.Stat(updateCachePath(state)); err == nil {
		t.Fatal("update-check.json written after tw init / tw dev")
	}

	// Control: the same setup does check after a command that talks to mgmt.
	os.MkdirAll(state, 0o700)
	updateNotice("ls", &stderr)
	if n := hits.Load(); n != 1 || !strings.Contains(stderr.String(), "tw 0.3.0 is out") {
		t.Fatalf("control (ls): %d request(s), stderr %q", n, stderr.String())
	}
}

// TestVersionReportsUpdateCheck: tw version --json says whether the check
// is on (#104).
func TestVersionReportsUpdateCheck(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	t.Setenv("CI", "")
	for _, tc := range []struct {
		version, optOut string
		want            bool
	}{
		{"0.2.0", "", true},
		{"0.2.0", "1", false},
		{"0.2.0-dev", "", false},
		{"dev", "", false},
	} {
		version = tc.version
		t.Setenv("TW_NO_UPDATE_CHECK", tc.optOut)
		out, err := captureStdout(t, func() error { return cmdVersion([]string{"--json"}) })
		var v struct {
			UpdateCheck *bool `json:"updateCheck"`
		}
		if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.UpdateCheck == nil || *v.UpdateCheck != tc.want {
			t.Fatalf("%+v: %v %q", tc, err, out)
		}
	}
}
