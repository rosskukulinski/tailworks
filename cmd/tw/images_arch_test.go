package main

import (
	"strings"
	"testing"
)

// TestImagePublicArchOnlyOnAdd: --arch is registered on `tw image public
// add` only, so ls and rm reject it before touching state or mgmt (#446).
func TestImagePublicArchOnlyOnAdd(t *testing.T) {
	for _, sub := range []string{"ls", "rm"} {
		args := []string{"public", sub, "--state-dir", t.TempDir(), "--arch", "amd64"}
		if sub == "rm" {
			args = append(args, "x")
		}
		err := cmdImage(args)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -arch") {
			t.Errorf("tw image public %s --arch: %v, want the unknown-flag error", sub, err)
		}
	}
}
