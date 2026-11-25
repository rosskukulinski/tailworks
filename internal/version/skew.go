package version

import "fmt"

// Skew policy (ADR-041 §7). N is the mgmt version.
//
//   - hostd: N−1 ≤ hostd ≤ N, counted in release lines (Semver.Line).
//     A host newer than mgmt is refused: hosts upgrade after mgmt.
//   - mgmt replicas: N−1 and N may run together during a roll.
//   - CLI: any version ≥ N−1; a newer CLI only gets a warning.
//
// Developer builds (Version "dev") are compatible with everything.

// SkewError explains why two versions may not run together.
type SkewError struct {
	Component, Got, Peer, PeerVersion string
	Reason                            string
}

func (e *SkewError) Error() string {
	return fmt.Sprintf("%s %s and %s %s: %s", e.Component, e.Got, e.Peer, e.PeerVersion, e.Reason)
}

// parseOrLegacy treats an empty version as Legacy (a component from before
// version reporting).
func parseOrLegacy(v string) (Semver, error) {
	if v == "" {
		v = Legacy
	}
	return Parse(v)
}

// CheckHost reports whether a hostd at hostV may run against mgmt at
// mgmtV. nil = within the window. An empty hostV is Legacy.
func CheckHost(hostV, mgmtV string) error {
	if IsDev(mgmtV) || hostV == Dev {
		return nil
	}
	h, err := parseOrLegacy(hostV)
	if err != nil {
		return &SkewError{"hostd", hostV, "tw-mgmt", mgmtV, err.Error()}
	}
	m, err := Parse(mgmtV)
	if err != nil {
		return nil // mgmt's own version unparseable: nothing to compare against
	}
	switch d := h.LinesBehind(m); {
	case d < 0:
		return &SkewError{"hostd", h.String(), "tw-mgmt", m.String(),
			fmt.Sprintf("the host is newer than mgmt; upgrade tw-mgmt to %s first (hosts upgrade after mgmt)", h.LineName())}
	case d > 1:
		return &SkewError{"hostd", h.String(), "tw-mgmt", m.String(),
			fmt.Sprintf("the host is more than one release line behind (mgmt %s supports hosts on %s and %s); upgrade the host", m.LineName(), prevLine(m), m.LineName())}
	}
	return nil
}

// CheckReplica reports whether a mgmt replica at otherV may run next to
// one at selfV (N−1 and N together, in either order).
func CheckReplica(otherV, selfV string) error {
	if IsDev(selfV) || otherV == Dev {
		return nil
	}
	o, err := parseOrLegacy(otherV)
	if err != nil {
		return nil
	}
	s, err := Parse(selfV)
	if err != nil {
		return nil
	}
	if d := o.LinesBehind(s); d > 1 || d < -1 {
		return &SkewError{"tw-mgmt", o.String(), "tw-mgmt", s.String(), "replicas more than one release line apart may not run together; finish the previous upgrade first"}
	}
	return nil
}

// CLIAdvice is what the CLI at cliV should tell its user about mgmt at
// mgmtV: "" (fine), or a warning. tooOld is set when the CLI is older
// than N−1 (in --json mode that is a version_skew error).
func CLIAdvice(cliV, mgmtV string) (msg string, tooOld bool) {
	if IsDev(cliV) || IsDev(mgmtV) {
		return "", false
	}
	c, err1 := Parse(cliV)
	m, err2 := Parse(mgmtV)
	if err1 != nil || err2 != nil {
		return "", false
	}
	switch d := c.LinesBehind(m); {
	case d > 1:
		return fmt.Sprintf("tw %s is too old for tw-mgmt %s (it supports CLIs from %s on); run `tw upgrade` or install tw %s", c, m, prevLine(m), m.LineName()), true
	case d < 0:
		return fmt.Sprintf("tw %s is newer than tw-mgmt %s: commands that need newer mgmt features will fail until mgmt is upgraded", c, m), false
	}
	return "", false
}

func prevLine(s Semver) string {
	if s.Major == 0 {
		if s.Minor == 0 {
			return "0.0"
		}
		return fmt.Sprintf("0.%d", s.Minor-1)
	}
	if s.Major == 1 {
		return "0.x"
	}
	return fmt.Sprint(s.Major - 1)
}
