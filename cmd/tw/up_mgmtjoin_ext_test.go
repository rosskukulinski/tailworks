package main_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestUpMgmtJoinKeyRemoved (#245): `tw up` hands mgmt the single-use join
// key in <state>/mgmt/join.env, and the file is gone after a failed start
// (control refuses the minted key: tw up reports control's refusal and
// stops the supervisor) and after a successful one (the same state dir,
// with the key control accepts).
func TestUpMgmtJoinKeyRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	const requiredKey = "not-the-minted-key"
	ctl.Server.RequireAuthKey = requiredKey // before any node talks to it
	api := httptest.NewServer(newFakeTS(t))
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-jk-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "oauth.env")
	writeFakeOAuthFile(t, keyFile)
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work}
	t.Setenv("TW_NO_UPDATE_CHECK", "1")
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, netd.Redact(string(b[max(0, len(b)-4000):])))
			}
		}
		e.Tw("down", "--purge")
		os.RemoveAll(state)
	})
	joinFile := filepath.Join(state, "mgmt", "join.env")
	up := []string{"up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--control-url", ctl.URL, "--tsapi-url", api.URL,
		"--hostname-prefix", "tw-jk-", "--mgmt-hostname", "tw-jk-mgmt", "--host-id", "tw-jk", "--json"}

	so, se, _, err := e.Tw(up...)
	if err == nil {
		t.Fatalf("tw up succeeded although control refuses mgmt's key:\n%s", so)
	}
	if out := so + se; !strings.Contains(out, "bootstrap_failed") || !strings.Contains(out, "invalid authkey") {
		t.Errorf("tw up's error doesn't carry control's refusal:\n%s\n%s", so, netd.Redact(se))
	}
	if _, err := os.Stat(joinFile); !os.IsNotExist(err) {
		t.Errorf("after a failed tw up, %s is still there (stat: %v)", joinFile, err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "supervisor.pid")); len(b) > 0 {
		t.Errorf("after a failed tw up, the supervisor still runs (pid %s)", b)
	}

	// The key control accepts, handed over the same way (--mgmt-authkey-file
	// is copied into join.env).
	good := filepath.Join(work, "mgmt-key.env")
	os.WriteFile(good, []byte("TS_AUTHKEY="+requiredKey+"\n"), 0o600)
	if so, se, _, err := e.Tw(append(up, "--mgmt-authkey-file", good)...); err != nil {
		t.Fatalf("tw up: %v\n%s\n%s", err, so, netd.Redact(se))
	}
	if _, err := os.Stat(joinFile); !os.IsNotExist(err) {
		t.Errorf("after tw up, %s is still there (stat: %v)", joinFile, err)
	}
}
