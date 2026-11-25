package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/schema"
)

// TestSecurityReleaseNotice (#338): a latest release whose notes have a
// Security section is called a security release by `tw version --check`
// (text and --json) and by the once-a-day notice; one without is not.
func TestSecurityReleaseNotice(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "0.2.0"
	now := time.Now()

	c := updateCache{CheckedAt: now, Latest: "0.3.0", Security: true}
	if l := updateNoticeLine(c, now); !strings.Contains(l, "tw 0.3.0 is out (this is 0.2.0) — security release, upgrade soon: ") {
		t.Fatalf("security notice: %q", l)
	}
	c.Security = false
	if l := updateNoticeLine(c, now); strings.Contains(l, "security") {
		t.Fatalf("plain notice: %q", l)
	}

	body := "# tw v0.3.0\n\n## Security\n\n### fix-1\n\nThe pinned workerd moves to a V8 with the fix for CVE-2026-0001.\n\n## Fixes\n\n### fix-2\n\nx\n\nInstall: see docs/INSTALL.md\n"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.3.0", "html_url": "https://example.test/v0.3.0", "body": body})
	}))
	defer gh.Close()
	t.Setenv("TW_RELEASE_API", gh.URL)
	t.Setenv("TW_REPO", "")
	state := t.TempDir()

	out, err := captureStdout(t, func() error { return cmdVersion([]string{"--check", "--state-dir", state}) })
	if err != nil || !strings.Contains(out, "tw 0.3.0 is out (this is 0.2.0) — security release, upgrade soon: https://example.test/v0.3.0") {
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
	if v["securityRelease"] != true {
		t.Fatalf("version --check --json: %s", out)
	}
	// The check left the marker in the notice's cache.
	if c := loadUpdateCache(state); c.Latest != "0.3.0" || !c.Security {
		t.Fatalf("cache: %+v", c)
	}

	// A body that only mentions security is not a security release.
	body = "# tw v0.3.0\n\n## Fixes\n\n### fix-2\n\nA security-minded refactor.\n"
	out, _ = captureStdout(t, func() error { return cmdVersion([]string{"--check", "--state-dir", state}) })
	if !strings.Contains(out, "tw 0.3.0 is out (this is 0.2.0): https://example.test/v0.3.0") || strings.Contains(out, "security release") {
		t.Fatalf("plain release: %q", out)
	}
	if c := loadUpdateCache(state); c.Security {
		t.Fatalf("cache kept the marker: %+v", c)
	}
}
