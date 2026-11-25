package main

import (
	"flag"
	"os"
	"path/filepath"
)

// The Headscale profile without the --control=headscale flag (#539): a
// Headscale server is always named by a non-default --control-url, so that
// plus a Headscale API key selects the profile. --control=headscale stays an
// accepted explicit spelling, and an explicit --control=tailscale always
// wins. With the default (Tailscale) control URL, --api-key-file is still
// the removed Tailscale API-key mint mode (ADR-042, mint_mode_removed).

// defaultHeadscaleFile is where tw up looks for the Headscale API key when
// no credential flag is given and --control-url names a Headscale server:
// $XDG_CONFIG_HOME/tailworks/headscale.env, else
// ~/.config/tailworks/headscale.env. Never prompted for or created.
func defaultHeadscaleFile() string {
	p := defaultOAuthFile() // up_defaults.go: same directory
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "headscale.env")
}

// headscaleInferred reports whether the Headscale profile follows from the
// other flags: an API key file and a non-default control URL, with no
// explicit --control.
func headscaleInferred(fs *flag.FlagSet, keyFile, controlURL string) bool {
	return keyFile != "" && controlURL != prodControl && !flagGiven(fs, "control")
}

// headscaleDefaultKey returns the default Headscale key file for a tw up on
// the default state dir that was given no credential flag, when
// --control-url names a Headscale server (or --control=headscale was
// given) and the file exists. A bare tw up never looks here, so a machine
// with both files keeps the OAuth path.
func headscaleDefaultKey(fs *flag.FlagSet, profile, controlURL string) string {
	if controlURL == prodControl || (flagGiven(fs, "control") && profile != controlHeadscale) {
		return ""
	}
	p := defaultHeadscaleFile()
	if p == "" {
		return ""
	}
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return p
}

// headscaleOnHint is the extra fix line of mint_mode_removed for an
// --api-key-file given with Tailscale's control URL.
const headscaleOnHint = "on Headscale, add --control-url <your Headscale server_url> (and --tailnet <base_domain>)"
