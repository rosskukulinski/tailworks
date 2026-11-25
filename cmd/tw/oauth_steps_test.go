package main

import (
	"strings"
	"testing"
)

// TestOAuthConsoleSteps (issue #442): the first-run prompt and
// mint_mode_required share the same steps, and they hold everything needed
// to create the client without the README: both console URLs, the one
// tagOwners line tw can't write, the policy scope with its least-privilege
// choice, and that scopes are fixed once the client exists.
func TestOAuthConsoleSteps(t *testing.T) {
	t.Parallel()
	steps := strings.Join(oauthConsoleSteps(), "\n")
	for _, want := range []string{
		consoleACLURL,
		consoleOAuthURL,
		`"tag:tw": ["autogroup:admin"]`,
		`existing "tagOwners" section`,
		"Auth Keys › Write",
		"Policy File › Write",
		"can't be changed on an existing client",
		"tw kill deletes the device",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("console steps lack %q:\n%s", want, steps)
		}
	}
	e := strings.Join(mintModeRequired().Lines, "\n")
	for _, s := range oauthConsoleSteps() {
		if !strings.Contains(e, s) {
			t.Errorf("mint_mode_required lacks the shared step %q", s)
		}
	}
	if !strings.Contains(e, "--oauth-client-file") {
		t.Errorf("mint_mode_required lacks --oauth-client-file:\n%s", e)
	}
}
