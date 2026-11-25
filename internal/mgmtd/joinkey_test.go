package mgmtd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestJoinKeyOnce pins the single-use join key handoff `tw up` uses
// (joinkey.go): mgmt deletes the key file once it has read it, after a
// successful start and after a failed one, records why a start failed,
// and refuses (and deletes) a key file that group or others can read.
func TestJoinKeyOnce(t *testing.T) {
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	writeKey := func(t *testing.T, dir string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, "join.env")
		if err := os.WriteFile(p, []byte("TS_AUTHKEY="+nettest.DummyKey+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, mode) // past the umask
		return p
	}
	gone := func(t *testing.T, p, when string) {
		t.Helper()
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s: the key file is still there (stat: %v)", when, err)
		}
	}
	opts := func(dir, control, keyFile string) Options {
		return Options{StateDir: filepath.Join(dir, "mgmt"), TSNetHostname: "tw-once-mgmt", TSNetControlURL: control,
			TSNetAuthKeyFile: keyFile, TSNetAuthKeyOnce: true, RegistryDir: "off", Version: "test", NoLoopback: true}
	}

	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		key := writeKey(t, dir, 0o600)
		o := opts(dir, ctl.URL, key)
		os.MkdirAll(o.StateDir, 0o700)
		os.WriteFile(StartErrFile(o.StateDir), []byte("stale\n"), 0o600)
		rt, err := Start(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		gone(t, key, "after a successful start")
		if _, err := os.Stat(StartErrFile(o.StateDir)); !os.IsNotExist(err) {
			t.Errorf("a successful start left %s", StartErrFile(o.StateDir))
		}
		if rt.Self.NodeID == "" {
			t.Error("joined, but no node ID")
		}
		// A restart: the file is gone, so no key; the saved node state is used.
		rt.Close()
		rt2, err := Start(context.Background(), o)
		if err != nil {
			t.Fatalf("restart without the key file: %v", err)
		}
		rt2.Close()
	})

	t.Run("join refused", func(t *testing.T) {
		strict, err := nettest.StartControl()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(strict.Close)
		strict.Server.RequireAuthKey = "some-other-key"
		dir := t.TempDir()
		key := writeKey(t, dir, 0o600)
		o := opts(dir, strict.URL, key)
		if _, err := Start(context.Background(), o); err == nil {
			t.Fatal("Start succeeded with a key control refuses")
		}
		gone(t, key, "after a refused join")
		b, err := os.ReadFile(StartErrFile(o.StateDir))
		if err != nil || !strings.Contains(string(b), "invalid authkey") {
			t.Errorf("start.err = %q, %v; want control's refusal", b, err)
		}
		if strings.Contains(string(b), nettest.DummyKey) {
			t.Error("start.err holds the key")
		}
	})

	t.Run("failure before the join", func(t *testing.T) {
		dir := t.TempDir()
		key := writeKey(t, dir, 0o600)
		o := opts(dir, ctl.URL, key)
		o.Custody = "bogus"
		if _, err := Start(context.Background(), o); err == nil {
			t.Fatal("Start succeeded with a bogus custody")
		}
		gone(t, key, "after a start that failed before the join")
		if _, err := os.Stat(StartErrFile(o.StateDir)); err != nil {
			t.Errorf("no start.err: %v", err)
		}
	})

	t.Run("readable by others", func(t *testing.T) {
		dir := t.TempDir()
		key := writeKey(t, dir, 0o644)
		o := opts(dir, ctl.URL, key)
		if _, err := Start(context.Background(), o); err == nil {
			t.Fatal("Start used a key file that others can read")
		}
		gone(t, key, "after refusing a 0644 key file")
	})
}
