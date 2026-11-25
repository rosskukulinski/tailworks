package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/npminstall"
)

// Which install prefix `tw upgrade` replaces (issue #63).
//
// It used to be the prefix this tailworks binary runs from. The first upgrade
// from a release without `tw upgrade` has to run the new CLI, usually
// from the extracted release directory, which looks like an install
// (VERSION, share/tailworks): the plan then compared the release with itself,
// and a real run would have installed into the extracted directory and
// restarted the real tailworks. Now the prefix is the one that owns the state
// dir: the binary the service or the running supervisor was started from,
// else the prefix the last tw up recorded in tw.json, else the state dir itself (install.sh's default prefix is the default
// state dir, ~/.tailworks). When that differs from the binary's own prefix,
// tw upgrade refuses unless --prefix names the prefix to upgrade.

// prefixCandidate is one place the state dir's install prefix may come from.
type prefixCandidate struct {
	Prefix string // an install prefix ("" if this source says nothing)
	From   string // where it came from, for messages
}

// upgradePrefixChoice is resolvePrefix's answer.
type upgradePrefixChoice struct {
	Prefix string // the prefix to upgrade ("" if none is known)
	From   string // why
	// Mismatch is set when this tailworks runs from another prefix (or none)
	// than the one that owns the state dir: a real run refuses then,
	// unless --prefix was given.
	Mismatch string
}

// isInstallPrefix reports whether dir is an install.sh prefix.
func isInstallPrefix(dir string) bool {
	if dir == "" {
		return false
	}
	if _, npm := npminstall.At(dir); npm { // npm_install.go: npm owns it
		return false
	}
	for _, f := range []string{"VERSION", filepath.Join("share", "tailworks")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

// prefixOfBinary is the install prefix a tailworks binary lives in, or "".
func prefixOfBinary(bin string) string {
	if bin == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	p := filepath.Dir(filepath.Dir(bin))
	if !isInstallPrefix(p) {
		return ""
	}
	return p
}

// samePath compares two directories after resolving symlinks (/tmp is
// /private/tmp on macOS).
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	norm := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return filepath.Clean(p)
	}
	return norm(a) == norm(b)
}

// resolvePrefix picks the prefix: --prefix if given, else the first
// candidate that names one, else the binary's own prefix.
func resolvePrefix(flagPrefix, exePrefix string, cands []prefixCandidate) (upgradePrefixChoice, error) {
	if flagPrefix != "" {
		p, err := filepath.Abs(flagPrefix)
		if err != nil {
			return upgradePrefixChoice{}, err
		}
		if !isInstallPrefix(p) {
			return upgradePrefixChoice{}, fail("bad_request", "--prefix "+flagPrefix+" is not an install.sh prefix (no VERSION and share/tailworks in it)")
		}
		return upgradePrefixChoice{Prefix: p, From: "--prefix"}, nil
	}
	for _, c := range cands {
		if c.Prefix == "" {
			continue
		}
		ch := upgradePrefixChoice{Prefix: c.Prefix, From: c.From}
		if !samePath(c.Prefix, exePrefix) {
			runs := "this tailworks doesn't run from an install prefix"
			if exePrefix != "" {
				runs = "this tailworks runs from " + exePrefix
			}
			ch.Mismatch = fmt.Sprintf("%s, but the tailworks that owns the state dir is installed in %s (%s)", runs, c.Prefix, c.From)
		}
		return ch, nil
	}
	if exePrefix == "" {
		return upgradePrefixChoice{}, nil
	}
	return upgradePrefixChoice{Prefix: exePrefix, From: "this tailworks' own prefix"}, nil
}

// statePrefixCandidates lists where the state dir's install prefix may
// come from, most reliable first.
func statePrefixCandidates(s *state) []prefixCandidate {
	var cs []prefixCandidate
	if inst, _ := serviceOf(s); inst {
		if b, err := os.ReadFile(serviceUnitPath()); err == nil {
			cs = append(cs, prefixCandidate{Prefix: prefixOfBinary(serviceBinary(string(b))), From: "the " + serviceKind() + " runs it"})
		}
	}
	if pid := s.supervisorPID(); pidAlive(pid) {
		cs = append(cs, prefixCandidate{Prefix: prefixOfBinary(processBinary(pid)), From: "the running supervisor, pid " + strconv.Itoa(pid)})
	}
	// What the last tw up recorded: after the live sources (which can't be
	// stale while tailworks runs), and only while it is still an install,
	// so a moved or removed one falls through.
	if isInstallPrefix(s.Cfg.InstallPrefix) {
		cs = append(cs, prefixCandidate{Prefix: s.Cfg.InstallPrefix, From: "tw.json, recorded by tw up"})
	}
	if isInstallPrefix(s.Dir) {
		cs = append(cs, prefixCandidate{Prefix: s.Dir, From: "the state dir is the install prefix"})
	}
	return cs
}

// upInstallPrefix is what tw up records as tw.json's installPrefix: the
// install.sh prefix this tw runs from, or "" (npm, a dev build).
func upInstallPrefix() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return prefixOfBinary(exe)
}

// serviceBinary is the tailworks binary a LaunchAgent plist or systemd unit
// (plistFor, unitFor) starts, or "".
func serviceBinary(def string) string {
	if _, rest, ok := strings.Cut(def, "<key>ProgramArguments</key>"); ok {
		_, rest, ok = strings.Cut(rest, "<string>")
		if !ok {
			return ""
		}
		v, _, ok := strings.Cut(rest, "</string>")
		if !ok {
			return ""
		}
		var out string
		if err := xml.Unmarshal([]byte("<s>"+v+"</s>"), &out); err != nil {
			return ""
		}
		return out
	}
	for _, ln := range strings.Split(def, "\n") {
		v, ok := strings.CutPrefix(ln, "ExecStart=")
		if !ok {
			continue
		}
		if strings.HasPrefix(v, `"`) {
			var b strings.Builder
			for i := 1; i < len(v); i++ {
				switch v[i] {
				case '\\':
					if i+1 < len(v) {
						i++
						b.WriteByte(v[i])
					}
				case '"':
					return b.String()
				default:
					b.WriteByte(v[i])
				}
			}
			return ""
		}
		bin, _, _ := strings.Cut(v, " ")
		return bin
	}
	return ""
}

// processBinary is a running process's executable path, or "".
func processBinary(pid int) string {
	if runtime.GOOS == "linux" {
		p, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		return strings.TrimSuffix(p, " (deleted)")
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		return ""
	}
	return p
}
