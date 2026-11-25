package main

import (
	"errors"
	"flag"
	"testing"
)

// upFlagSet is `tw up`'s own flag set (from its --help request), so a
// renamed flag fails here.
func upFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	var h *helpRequest
	if err := cmdUp([]string{"--help"}); !errors.As(err, &h) {
		t.Fatalf("tw up --help: %v", err)
	}
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	h.f.VisitAll(func(f *flag.Flag) {
		if f.Name != "help" {
			fs.Var(f.Value, f.Name, f.Usage)
			f.Value.Set(f.DefValue)
		}
	})
	return fs
}

// TestUpKeepsSavedSettings: `tw up` again on a set-up state dir (what
// tw upgrade runs to restart tailworks: `tw up --yes`) keeps the settings
// it was set up with; a flag given again wins (issue #111).
func TestUpKeepsSavedSettings(t *testing.T) {
	warm, pool := 0, 3
	c := Config{ControlURL: "http://control.test", TSAPIURL: "http://api.test", HostID: "h-saved", HostnamePrefix: "tw-x-",
		Ephemeral: true, InsecureSharedHost: true, AllowSiblingEgress: true, LocalDev: true, PortMapping: true,
		Heartbeat: "2s", KillTombstone: "20s", DeviceDeleteDelay: "9s", PushPort: 7575, WarmKeys: &warm, PoolNodes: &pool,
		Workerd: "/old/release/bin/workerd"}
	want := map[string]string{"control-url": "http://control.test", "tsapi-url": "http://api.test", "host-id": "h-saved",
		"hostname-prefix": "tw-x-", "ephemeral": "true", "insecure-shared-host": "true", "allow-sibling-egress": "true",
		"local-dev": "true", "port-mapping": "true", "warm-keys": "0", "pool-nodes": "3",
		"workerd": ""} // not kept: the install's workerd
	fs := upFlagSet(t)
	keepSavedUpFlags(fs, c)
	for name, v := range want {
		f := fs.Lookup(name)
		if f == nil {
			t.Fatalf("tw up has no --%s", name)
		}
		if got := f.Value.String(); got != v {
			t.Errorf("--%s = %q, want the saved %q", name, got, v)
		}
	}

	fs = upFlagSet(t)
	if err := fs.Parse([]string{"--host-id", "h-given", "--ephemeral=false", "--warm-keys", "7"}); err != nil {
		t.Fatal(err)
	}
	keepSavedUpFlags(fs, c)
	for name, v := range map[string]string{"host-id": "h-given", "ephemeral": "false", "warm-keys": "7", "hostname-prefix": "tw-x-"} {
		if got := fs.Lookup(name).Value.String(); got != v {
			t.Errorf("--%s = %q, want %q", name, got, v)
		}
	}

	// State from before a setting existed keeps the default.
	fs = upFlagSet(t)
	keepSavedUpFlags(fs, Config{HostID: "h-old"})
	for name, v := range map[string]string{"warm-keys": "5", "pool-nodes": "1", "port-mapping": "false"} {
		if got := fs.Lookup(name).Value.String(); got != v {
			t.Errorf("old state: --%s = %q, want the default %q", name, got, v)
		}
	}
}
