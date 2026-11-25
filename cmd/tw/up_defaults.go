package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"golang.org/x/term"
)

// The defaults that make the first `tw up` a bare `tw up`: the OAuth client
// lives in a standard file (asked for and saved on first run), a credential
// file that is too open is tightened rather than refused, and on the default
// state dir tailworks is installed as a login service.
//
// These apply only to the default state dir (~/.tailworks, no --state-dir
// and no $TW_STATE_DIR): tests and extra instances name their own state dir
// and must never pick up the operator's credential or install a service.

// defaultOAuthFile is where tw up looks for the OAuth client when no
// --oauth-client-file is given: $XDG_CONFIG_HOME/tailworks/oauth.env, else
// ~/.config/tailworks/oauth.env.
func defaultOAuthFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tailworks", "oauth.env")
}

// isDefaultStateDir reports whether dir is ~/.tailworks with no override.
func isDefaultStateDir(dir string, flagVal string) bool {
	if flagVal != "" || os.Getenv("TW_STATE_DIR") != "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return filepath.Clean(dir) == filepath.Join(home, ".tailworks")
}

// promptOAuthSecret asks for the OAuth client secret on a terminal and saves
// it 0600 at path. The client ID is the secret's third segment, so the
// secret alone is enough. ok is false when there is no terminal to ask on.
// cmd is the command asking ("tw up"); tailnet names the tailnet whose
// client it needs ("" for the operator's own: "your tailnet").
func promptOAuthSecret(path string, out io.Writer, cmd, tailnet string) (ok bool, err error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, nil
	}
	if tailnet == "" {
		tailnet = "your tailnet"
	}
	fmt.Fprintln(out, cmd+" needs a Tailscale OAuth client of "+tailnet+" (its secret stays in a 0600 file on this machine). In the admin console:")
	for _, s := range oauthConsoleSteps() { // oauth.go
		fmt.Fprintln(out, "  "+s)
	}
	fmt.Fprintf(out, "  4. Paste its Client Secret (tskey-client-…; input is hidden): ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return true, err
	}
	secret := strings.TrimSpace(string(b))
	if !strings.HasPrefix(secret, tsapi.OAuthSecretPrefix) {
		return true, fail("bad_request", "that is not an OAuth client secret (it starts with "+tsapi.OAuthSecretPrefix+")").
			withFix("copy the Client Secret (not the Client ID) from the Trust credentials page, then " + cmd + " again")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return true, err
	}
	if err := os.WriteFile(path, []byte("TS_OAUTH_CLIENT_SECRET="+secret+"\n"), 0o600); err != nil {
		return true, err
	}
	fmt.Fprintf(out, "› oauth    saved to %s (readable by you only)\n", path)
	return true, nil
}

// tightenSecretFile makes a credential file readable by its owner only when
// it is a regular file this user owns and its mode lets others in. Anything
// else is left for the loader to report.
func tightenSecretFile(path string) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 == 0 {
		return
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return
	}
	_ = os.Chmod(path, 0o600)
}

// flagGiven reports whether the flag was set on the command line.
func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}
