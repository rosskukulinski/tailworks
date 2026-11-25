// Package version is the one place a tailworks binary learns its release
// version, commit and the contracts it speaks (ADR-041
// §1). Version and Commit are set by the linker:
//
//	go build -ldflags "-X github.com/rosskukulinski/tailworks/internal/version.Version=0.2.0 \
//	    -X github.com/rosskukulinski/tailworks/internal/version.Commit=<sha>"
//
// scripts/release.sh and `make build` do this for every binary. A plain
// `go build` or `go test` leaves Version "dev", which skew checks treat as
// compatible with everything (a developer build is never refused).
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the release version ("0.2.0", "0.2.0-1a2b3c4d-dirty"), or
// "dev" for an unstamped build.
var Version = "dev"

// Commit is the git commit the binary was built from ("" = unknown).
var Commit = ""

// Dev is the Version of an unstamped build.
const Dev = "dev"

// Protocol versions tailworks speaks (ADR-041 §2). Each is
// an integer bumped only on an incompatible change to that contract;
// additive changes (new optional fields, new messages gated by a feature)
// keep it.
const (
	// PushProtocol is mgmt → hostd's push API (hostd/http.go /v1/push/*).
	PushProtocol = 1
	// GuestProtocol is hostd ↔ tw-guestd over vsock (guest/proto).
	GuestProtocol = 1
	// VZProtocol is hostd ↔ tw-vz (hostd/vm/vzapi).
	VZProtocol = 1
	// RESTMajor is the mgmt REST API's path major (/v1).
	RESTMajor = 1
	// CLIJSONMajor is the major of schema/cli.schema.json (stable --json).
	CLIJSONMajor = 1
)

// IsDev reports whether v is an unstamped developer build.
func IsDev(v string) bool { return v == "" || v == Dev }

// Semver is a parsed MAJOR.MINOR.PATCH; anything after a '-' or '+'
// (a commit suffix, "-dirty") is kept in Suffix and ignored by
// comparisons.
type Semver struct {
	Major, Minor, Patch int
	Suffix              string
}

// Parse parses "v0.2.1", "0.2.1" or "0.2.1-1a2b3c4d-dirty".
func Parse(v string) (Semver, error) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out Semver
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s, out.Suffix = s[:i], s[i:]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", v)
	}
	nums := [3]*int{&out.Major, &out.Minor, &out.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Semver{}, fmt.Errorf("version %q: %q is not a number", v, p)
		}
		*nums[i] = n
	}
	return out, nil
}

// MustParse is Parse for constants; it panics on error.
func MustParse(v string) Semver {
	s, err := Parse(v)
	if err != nil {
		panic(err)
	}
	return s
}

func (s Semver) String() string { return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch) }

// Compare orders by major, minor, patch (suffix ignored): -1, 0 or 1.
func (s Semver) Compare(o Semver) int {
	for _, d := range [3]int{s.Major - o.Major, s.Minor - o.Minor, s.Patch - o.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}

// LinesBehind is how many release lines s is behind o (negative: ahead).
// A release line is the minor before 1.0 (0.MINOR) and the major from 1.0
// on (ADR-041 §9). Two versions on the same line are
// compatible in every direction; one line apart is the N−1 window. The
// last 0.x line is the one before 1: any 0.x counts as one line behind
// 1.y, since the final 0.x minor isn't known in advance.
func (s Semver) LinesBehind(o Semver) int {
	switch {
	case s.Major == 0 && o.Major == 0:
		return o.Minor - s.Minor
	case s.Major == 0:
		return o.Major // 0.x is one line behind 1
	case o.Major == 0:
		return -s.Major
	}
	return o.Major - s.Major
}

// LineName names a release line: "0.2" before 1.0, "1" after.
func (s Semver) LineName() string {
	if s.Major == 0 {
		return fmt.Sprintf("0.%d", s.Minor)
	}
	return strconv.Itoa(s.Major)
}

// Legacy is the version assumed for a component that reports none: every
// tailworks before version reporting was 0.1.x.
const Legacy = "0.1.0"

// String is "<name> <Version> (<commit>)", what every binary's --version
// prints.
func String(name string) string {
	s := name + " " + Version
	if Commit != "" {
		c := Commit
		if len(c) > 12 {
			c = c[:12]
		}
		s += " (" + c + ")"
	}
	return s
}

// Asked reports whether args (os.Args) is exactly `<bin> --version` (or
// -version). Each binary's main prints String and exits when it is.
func Asked(args []string) bool {
	return len(args) == 2 && (args[1] == "--version" || args[1] == "-version")
}

// Protocols is the protocol map a component reports (HostRegistration
// "protocols", release.json "protocols"). helper is hostd/vm/helperapi's
// Version (passed in: this package imports nothing of tailworks').
func Protocols(helper int) map[string]int {
	return map[string]int{
		"cliJSON": CLIJSONMajor,
		"guest":   GuestProtocol,
		"helper":  helper,
		"push":    PushProtocol,
		"rest":    RESTMajor,
		"vz":      VZProtocol,
	}
}
