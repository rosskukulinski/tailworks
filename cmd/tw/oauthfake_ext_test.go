package main_test

import (
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// The OAuth client every fakeTS knows by default (newFakeTS) and
// writeFakeOAuthFile writes.
var fakeOAuthID, fakeOAuthSecret = cliflow.FakeOAuthID, cliflow.FakeOAuthSecret

func writeFakeOAuthFile(t *testing.T, path string) string { return cliflow.WriteFakeOAuthFile(t, path) }
