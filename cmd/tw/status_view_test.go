package main

import (
	"slices"
	"strings"
	"testing"
)

// printed is fn's stdout (captureStdout: whoami_test.go).
func printed(t *testing.T, fn func()) string {
	t.Helper()
	o, _ := captureStdout(t, func() error { fn(); return nil })
	return o
}

func TestObjectsLabel(t *testing.T) {
	for _, c := range []struct {
		in   map[string]int
		want string
	}{
		{map[string]int{}, "0 objects"},
		{map[string]int{"ready": 1}, "1 object"},
		{map[string]int{"ready": 3}, "3 objects"},
		{map[string]int{"ready": 2, "pending": 1}, "3 objects (2 ready, 1 pending)"},
		{map[string]int{"killed": 1, "ready": 2, "failed": 0, "pending": 1}, "4 objects (2 ready, 1 killed, 1 pending)"},
	} {
		if got := objectsLabel(c.in); got != c.want {
			t.Errorf("objectsLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMgmtExtras(t *testing.T) {
	if got := mgmtExtras(0, 0, 0); got != "" {
		t.Errorf("all zero: %q", got)
	}
	if got := mgmtExtras(2, 1, 5); got != " · mint queue 2 · 1 pending device delete · 5 warm keys in custody" {
		t.Errorf("nonzero: %q", got)
	}
	// Warm keys in custody are shown whenever there are any (H25).
	if got := mgmtExtras(0, 0, 1); got != " · 1 warm key in custody" {
		t.Errorf("one warm key: %q", got)
	}
}

func TestUnsafeFlags(t *testing.T) {
	if got := unsafeFlags(Config{Ephemeral: true, AllowSiblingEgress: true}); got == nil || len(got) != 0 {
		t.Fatalf("no unsafe flags: %#v, want []", got)
	}
	c := Config{InsecureSharedHost: true, LocalDev: true, AllowExperimentalFlags: true, Ephemeral: true}
	if got := unsafeFlags(c); !slices.Equal(got, []string{"insecure-shared-host", "allow-experimental-flags", "local-dev"}) {
		t.Fatalf("unsafeFlags: %v", got)
	}
	got := printed(t, func() { printSavedFlags(c, "/st", 9, nil) })
	for _, want := range []string{
		"! unsafe   --insecure-shared-host (tenants NOT isolated), --allow-experimental-flags",
		"--local-dev (dev-only transport; fixed for this state dir)",
		"saved in /st/tw.json; clear with tw down, then tw up --insecure-shared-host=false --allow-experimental-flags=false",
		"› saved    --ephemeral · kept from /st/tw.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Nothing saved: nothing printed.
	if o := printed(t, func() { printSavedFlags(Config{}, "/st", 12, nil) }); o != "" {
		t.Errorf("no flags: %q", o)
	}
	// The running host disagrees with tw.json.
	on := true
	got = printed(t, func() { printSavedFlags(Config{}, "/st", 12, &on) })
	if !strings.Contains(got, "! unsafe      the running host has --insecure-shared-host=true but /st/tw.json says false") {
		t.Errorf("mismatch not marked: %q", got)
	}
}
