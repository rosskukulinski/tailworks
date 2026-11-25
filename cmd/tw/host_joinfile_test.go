package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/hostdrun"
)

// TestHostJoinFileContent: tw host add's join file carries what tw-hostd
// needs to start with no flags (#488), and tw-hostd reads it back.
func TestHostJoinFileContent(t *testing.T) {
	t.Parallel()
	hj := hostJoin{AuthKey: "tskey-auth-x", RegistrationSecret: "sec", ControlURL: "https://hs.example", KeyTagsOnly: true}
	b := hj.hostJoinFileContent("h2", "http://tw-mgmt.example.ts.net")
	for _, l := range []string{"TS_AUTHKEY=tskey-auth-x", "TW_REGISTRATION_SECRET=sec", "TW_HOST_ID=h2", "TW_MGMT_URL=http://tw-mgmt.example.ts.net",
		"TW_TSNET_CONTROL_URL=https://hs.example", "TW_TSNET_KEY_TAGS_ONLY=1"} {
		if !strings.Contains(string(b), l+"\n") {
			t.Fatalf("join file lacks %s:\n%s", l, b)
		}
	}
	p := filepath.Join(t.TempDir(), "h2.join.env")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := hostdrun.ReadJoinSettings(p)
	if want := (hostdrun.JoinSettings{HostID: "h2", MgmtURL: "http://tw-mgmt.example.ts.net", ControlURL: "https://hs.example", KeyTagsOnly: true}); got != want {
		t.Fatalf("read back %+v, want %+v", got, want)
	}
	// Tailscale: no key-tags-only line.
	hj.KeyTagsOnly = false
	if b := hj.hostJoinFileContent("h2", ""); strings.Contains(string(b), "KEY_TAGS_ONLY") || strings.Contains(string(b), "TW_MGMT_URL") {
		t.Fatalf("tailscale join file:\n%s", b)
	}
}
