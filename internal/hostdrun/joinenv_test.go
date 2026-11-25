package hostdrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
)

// TestJoinSettingsResolve (#488): a join file with the new keys is enough to
// start tw-hostd with no other flags; an older two-key file still needs
// -mgmt; flags win; the settings saved at the join serve later starts; a
// node with state but no known control URL fails fast.
func TestJoinSettingsResolve(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const secrets = "TS_AUTHKEY=tskey-auth-x\nTW_REGISTRATION_SECRET=s\n"
	newFile := write("new.env", secrets+"TW_HOST_ID=h2\nTW_MGMT_URL=http://tw-mgmt.example.ts.net\nTW_TSNET_CONTROL_URL=https://hs.example\nTW_TSNET_KEY_TAGS_ONLY=1\nTW_FUTURE=ignored\n")
	oldFile := write("old.env", secrets)

	// The new join file alone.
	o := Options{DataDir: filepath.Join(dir, "a"), JoinTokenFile: newFile}
	src, err := resolveJoinSettings(&o, dir)
	if err != nil {
		t.Fatal(err)
	}
	if o.HostID != "h2" || o.MgmtAddr != "http://tw-mgmt.example.ts.net" || o.TSNetControlURL != "https://hs.example" || !o.TSNetKeyTagsOnly {
		t.Fatalf("from the join file: %+v", o)
	}
	if src != "host=join-file mgmt=join-file control=join-file" {
		t.Fatalf("source %q", src)
	}
	if o.TSNetDir != filepath.Join(dir, "a", "tsnet") {
		t.Fatalf("tsnet dir %s", o.TSNetDir)
	}

	// Flags win over the file.
	o = Options{DataDir: filepath.Join(dir, "a"), JoinTokenFile: newFile, HostID: "flag-h", MgmtAddr: "http://m", TSNetControlURL: "https://c"}
	if src, err = resolveJoinSettings(&o, dir); err != nil || o.HostID != "flag-h" || o.MgmtAddr != "http://m" || o.TSNetControlURL != "https://c" ||
		src != "host=flag mgmt=flag control=flag" {
		t.Fatalf("flags: %+v %q %v", o, src, err)
	}

	// An older two-key join file still needs -mgmt; the control URL
	// defaults to Tailscale's for a node that hasn't joined yet.
	o = Options{DataDir: filepath.Join(dir, "b"), JoinTokenFile: oldFile}
	if _, err := resolveJoinSettings(&o, dir); err == nil || !strings.Contains(err.Error(), "-mgmt is required") {
		t.Fatalf("old join file without -mgmt: %v", err)
	}
	o = Options{DataDir: filepath.Join(dir, "b"), JoinTokenFile: oldFile, MgmtAddr: "http://m"}
	if _, err := resolveJoinSettings(&o, dir); err != nil || o.TSNetControlURL != hostd.DefaultControlURL || o.TSNetKeyTagsOnly {
		t.Fatalf("old join file with -mgmt: %+v %v", o, err)
	}

	// After the join: the saved settings serve a start with no flags and
	// no join file (it was deleted).
	tsDir := filepath.Join(dir, "c", "tsnet")
	if err := SaveJoinSettings(tsDir, JoinSettings{HostID: "h3", MgmtURL: "http://mg", ControlURL: "https://hs.example", KeyTagsOnly: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(joinEnvFile(tsDir)); st.Mode().Perm() != 0o600 {
		t.Fatalf("join.env mode %v", st.Mode().Perm())
	}
	os.WriteFile(filepath.Join(tsDir, "tailscaled.state"), []byte("{}"), 0o600)
	o = Options{DataDir: filepath.Join(dir, "c"), JoinTokenFile: filepath.Join(dir, "gone.env")}
	if src, err = resolveJoinSettings(&o, dir); err != nil || o.HostID != "h3" || o.MgmtAddr != "http://mg" || o.TSNetControlURL != "https://hs.example" ||
		!o.TSNetKeyTagsOnly || src != "host=saved mgmt=saved control=saved" {
		t.Fatalf("saved: %+v %q %v", o, src, err)
	}

	// A node with state and no known control URL: the hard error stays.
	tsDir = filepath.Join(dir, "d", "tsnet")
	os.MkdirAll(tsDir, 0o700)
	os.WriteFile(filepath.Join(tsDir, "tailscaled.state"), []byte("{}"), 0o600)
	o = Options{DataDir: filepath.Join(dir, "d"), MgmtAddr: "http://m"}
	if _, err := resolveJoinSettings(&o, dir); err == nil || !strings.Contains(err.Error(), "-tsnet-control-url is required") {
		t.Fatalf("state without a control URL: %v", err)
	}
}
