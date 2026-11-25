package hostd

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// legacyProfileName is the profile file of an instance under
// "seatbelt-legacy" (sandboxOf tells it from the strict sandbox.sb).
const legacyProfileName = "sandbox-legacy.sb"

// seatbeltLegacyProfile is the Seatbelt profile from before issue #121, kept
// for one release behind -sandbox seatbelt-legacy: (allow default), reads,
// writes and unix socket connects denied under each of deny, then allowed
// under each of own; reads of each of readOnly and below readOnlyTrees.
// TCP, signals, exec and writes elsewhere are not restricted.
func seatbeltLegacyProfile(own, readOnly, readOnlyTrees, deny []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	for _, r := range realPaths(deny) {
		fmt.Fprintf(&b, "(deny file-read* file-write* (subpath %s))\n", sbQuote(r))
		fmt.Fprintf(&b, "(deny network-outbound (remote unix-socket (subpath %s)))\n", sbQuote(r))
	}
	for _, m := range ancestors(append(append([]string{}, own...), readOnlyTrees...)) {
		fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", sbQuote(m))
	}
	for _, o := range readOnly {
		for _, p := range []string{o, realPath(o)} {
			fmt.Fprintf(&b, "(allow file-read* (literal %s))\n", sbQuote(p))
		}
	}
	for _, o := range readOnlyTrees {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", sbQuote(realPath(o)))
	}
	for _, o := range own {
		r := realPath(o)
		fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s))\n", sbQuote(r))
		fmt.Fprintf(&b, "(allow network-outbound (remote unix-socket (subpath %s)))\n", sbQuote(r))
	}
	return b.String()
}

// realPaths resolves each non-empty path (realPath), without duplicates.
func realPaths(ps []string) []string {
	var out []string
	for _, p := range ps {
		if p == "" {
			continue
		}
		if r := realPath(p); !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// ancestors are the resolved parent dirs of each of ps (not "/"), which
// stay stat-able so workerd and node can resolve their paths.
func ancestors(ps []string) []string {
	var out []string
	for _, o := range ps {
		for d := filepath.Dir(realPath(o)); d != "/" && d != "."; d = filepath.Dir(d) {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}
