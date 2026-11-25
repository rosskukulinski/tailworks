package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/procsb"
)

// Where tailworks' state and credentials may live on a Linux host with the
// Landlock sandbox (issue #342). Landlock has no deny rules: every workload
// may read the OS trees (/usr, /etc, /opt, …), so hostd's deny list protects
// the state dir, run dir and credential files only while they are outside
// those trees. tw up and tw tenant add|set refuse a path inside one
// (sandbox_layout_unsafe), and tw doctor probes the real paths.

// sandboxDenyList is what tw up denies to every sandbox: tailworks' state
// (mgmt custody, every tenant's node keys and DO storage), its sockets and
// the OAuth client file.
func (s *state) sandboxDenyList() []string {
	deny := []string{filepath.Join(s.Dir, "mgmt"), filepath.Join(s.Dir, "hostd"), s.logDir(), s.configPath(), s.runDir()}
	if s.Cfg.OAuthClientFile != "" {
		deny = append(deny, s.Cfg.OAuthClientFile) // the OAuth client secret (also denied via the tenant list)
	}
	return deny
}

// sandboxLayoutPaths are the paths hostd's deny list would hold for this
// state dir, each with its role: the state and run dirs, every tenant's
// credential file (tw.json's view, once tw up has run) and the home dir.
// With no state at all only the home dir.
func sandboxLayoutPaths(s *state) []procsb.LayoutPath {
	var paths []procsb.LayoutPath
	if s != nil && s.Dir != "" {
		paths = append(paths,
			procsb.LayoutPath{What: "state dir", Path: s.Dir},
			procsb.LayoutPath{What: "run dir", Path: s.runDir()},
		)
	}
	if s != nil && s.configured() {
		for _, t := range s.tenants() {
			for _, f := range []string{t.APIKeyFile, t.OAuthClientFile} {
				if f != "" {
					paths = append(paths, procsb.LayoutPath{What: "credential file of tenant " + t.Tailnet, Path: f})
				}
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		paths = append(paths, procsb.LayoutPath{What: "home dir", Path: home})
	}
	return paths
}

// checkSandboxLayout refuses, when tw up would run workloads under
// Landlock, any of paths that a sandboxed workload could read.
func checkSandboxLayout(paths []procsb.LayoutPath) error {
	if sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto); sb != hostd.SandboxLandlock {
		return nil
	}
	err := procsb.CheckLayout(paths, procsb.ReadableTrees())
	var le *procsb.LayoutError
	if !errors.As(err, &le) {
		return err
	}
	return layoutFail(le)
}

// layoutFail is the CLI error for a LayoutError.
func layoutFail(le *procsb.LayoutError) *cliErr {
	where := le.Path
	if le.Real != "" && le.Real != le.Path {
		where += " (" + le.Real + ")"
	}
	return fail(procsb.LayoutErrorCode, "the "+le.What+" "+where+" is under "+le.Tree+", which every sandboxed workload may read",
		"Linux's Landlock sandbox has no deny rules: tailworks keeps workloads out of its state and credentials only by not allowing them, and "+le.Tree+" is allowed (the OS trees "+strings.Join(procsb.ReadableTrees(), ", ")+" are)").
		withFix(layoutFixFor(le.What)...)
}

func layoutFixFor(what string) []string {
	trees := strings.Join(procsb.ReadableTrees(), ", ")
	switch {
	case strings.HasPrefix(what, "credential file"):
		return []string{"move the file outside " + trees + " (for example ~/.config/tailworks/oauth.env, mode 0600), then run the command again with the new path"}
	case what == "home dir":
		return []string{"give the tailworks user a home dir outside " + trees + " (usermod -d /var/lib/tailworks <user>), or run tw with HOME set to one"}
	}
	return []string{"use a state dir outside " + trees + " (the default ~/.tailworks, or tw up --state-dir /var/lib/tailworks)", "or deploy with tier \"microvm\""}
}

// checkUpLayout is tw up's check: the state dir, run dir, this tenant's
// credential file and the home dir.
func checkUpLayout(s *state, oauthFile, keyFile string) error {
	paths := []procsb.LayoutPath{
		{What: "state dir", Path: s.Dir},
		{What: "run dir", Path: s.runDir()},
	}
	for _, f := range []string{oauthFile, keyFile} {
		if f != "" {
			abs, _ := filepath.Abs(f)
			paths = append(paths, procsb.LayoutPath{What: "credential file", Path: abs})
		}
	}
	return checkSandboxLayout(append(paths, sandboxLayoutPaths(nil)...))
}

// doctorLandlockLayout is tw doctor's check of the real deny list: every
// path hostd would deny for this state dir is probed under the profile
// (a dir listed, a file read), and one a workload can reach fails the
// check naming the path and the tree it is under; a path not created yet
// is checked by location. Returns "" when all is well.
func doctorLandlockLayout(s *state, probe func(op, arg string) string, osv string) (string, string, string) {
	paths := sandboxLayoutPaths(s)
	for _, p := range paths {
		st, err := os.Stat(p.Path)
		if err != nil {
			continue
		}
		op := procsb.ProbeRead
		if st.IsDir() {
			op = procsb.ProbeList
		}
		if r := probe(op, p.Path); r == "ok" {
			var le *procsb.LayoutError
			if !errors.As(procsb.CheckLayout([]procsb.LayoutPath{p}, procsb.ReadableTrees()), &le) {
				return "fail", "Landlock did not enforce on " + osv + " (the " + p.What + " " + p.Path + " was readable)", "report this; tenants are not confined on this machine"
			}
			e := layoutFail(le)
			return "fail", "every sandboxed workload can read the " + p.What + " " + p.Path + " on " + osv + ": " + e.Lines[0], strings.Join(e.Fix, "; ")
		}
	}
	var le *procsb.LayoutError
	if errors.As(procsb.CheckLayout(paths, procsb.ReadableTrees()), &le) {
		e := layoutFail(le)
		return "fail", e.Message + " once it exists on " + osv + ": " + e.Lines[0], strings.Join(e.Fix, "; ")
	}
	return "", "", ""
}
