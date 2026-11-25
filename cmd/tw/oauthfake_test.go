package main

import (
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// writeFakeOAuthFile writes the hermetic tests' OAuth client file
// (cliflow.FakeOAuthID; mgmt only validates its shape).
func writeFakeOAuthFile(t *testing.T, path string) string { return cliflow.WriteFakeOAuthFile(t, path) }
