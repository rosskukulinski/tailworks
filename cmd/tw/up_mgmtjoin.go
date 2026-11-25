package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/internal/mgmtd"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// mgmt joins its own node (#245). `tw up` used to join mgmt's node in the
// CLI, close it, and have tw-mgmt open it again: two tsnet starts, each
// about a second of round trips to production control. Now `tw up` mints
// the single-use join key and hands it to mgmt in mgmtJoinFile (0600, in
// mgmt's 0700 state dir); mgmt reads it and deletes the file before it
// joins (internal/mgmtd/joinkey.go), and a failed start deletes it too.
// tw up also deletes it when it returns, so a key mgmt never read doesn't
// outlive the command. The key is pre-authorized, tagged and expires
// within 5 minutes either way.

// mgmtJoinFile is the single-use handoff of mgmt's join key.
func (s *state) mgmtJoinFile() string { return filepath.Join(s.Dir, "mgmt", "join.env") }

// mgmtStartErr is where mgmt records why its start failed (mgmtd.StartErrFile).
func (s *state) mgmtStartErr() string { return mgmtd.StartErrFile(filepath.Join(s.Dir, "mgmt")) }

// firstStartPending: tw up saved the config and started the supervisor,
// but mgmt has not joined yet, so the tailnet (read from mgmt's node when
// the OAuth client can't list devices) is not known.
func (s *state) firstStartPending() bool {
	return !s.configured() && s.Cfg.MgmtHostname != "" && s.Cfg.MintMode != ""
}

// mgmtNeedsKey: mgmt's node has no saved login, as far as tw up knows.
func (s *state) mgmtNeedsKey() bool {
	return !isFile(filepath.Join(s.mgmtTsnetDir(), "tailscaled.state")) || s.Cfg.MgmtNodeID == ""
}

// writeMgmtJoinKey writes the key for mgmt's next start (0600, atomic).
func (s *state) writeMgmtJoinKey(key string) error {
	if err := os.MkdirAll(filepath.Dir(s.mgmtJoinFile()), 0o700); err != nil {
		return err
	}
	tmp := s.mgmtJoinFile() + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte("TS_AUTHKEY="+key+"\n"), 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.mgmtJoinFile())
}

// mgmtStartFailure is mgmt's recorded start error, if any.
func (s *state) mgmtStartFailure() (string, bool) {
	b, err := os.ReadFile(s.mgmtStartErr())
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// needsLogin: mgmt's node was logged out (removed in the admin console,
// say) and no key was handed to it.
func needsLogin(msg string) bool { return strings.Contains(msg, "needs an interactive login") }

// joinFailure turns mgmt's start error into tw up's error.
func joinFailure(hostname string, tags []string, msg string) error {
	if tsapi.IsTagsNotPermitted(errors.New(msg)) {
		return policyMissingTagOwners("the control server refused the join of "+hostname+" with tags "+strings.Join(tags, ", "), errors.New(msg), nil)
	}
	e := fail("bootstrap_failed", "tw-mgmt failed to start", msg).withFix("tw logs --component mgmt")
	if strings.Contains(strings.ToLower(msg), "tag") {
		e.Fix = append(e.Fix, "the credential must be allowed to own "+strings.Join(tags, ", ")+" (tagOwners); add the snippet printed above, then run tw up again")
	}
	return e
}

// stopFailedStart stops the supervisor tw up just started after mgmt
// failed to start: through the service manager when it runs as the
// service, else with SIGTERM. A crash-looping service would otherwise
// keep retrying a join that can't succeed. On the state dir's first tw up
// the service is removed, as when tw up used to fail its own join before
// installing it.
func stopFailedStart(s *state, firstUp bool) {
	pid := s.supervisorPID()
	inst, _ := serviceOf(s)
	switch {
	case inst && firstUp:
		removeService()
	case inst:
		serviceStop()
	case pid > 0:
		syscall.Kill(pid, syscall.SIGTERM)
	}
	for t0 := time.Now(); pidAlive(pid) && time.Since(t0) < 30*time.Second; {
		time.Sleep(20 * time.Millisecond)
	}
}

// checkMgmtNode checks mgmt's node after its join (as tw up's own join
// used to): the tag, the tailnet, and no funnel attribute. It returns the
// tailnet.
func checkMgmtNode(lb *mgmt.Loopback, control, tailnet string) (string, error) {
	if !slices.Contains(lb.Tags, mgmt.MgmtTag) && control == prodControl {
		return "", fail("bootstrap_failed", "mgmt's node joined without the "+mgmt.MgmtTag+" tag (tags: "+strings.Join(lb.Tags, ",")+")",
			"the credential is not a tagOwner of "+mgmt.MgmtTag+", so control dropped the tag").
			withFix("add to tagOwners: \"" + mgmt.MgmtTag + "\": [\"autogroup:admin\", \"tag:tw\"], then remove the node " + lb.DNSName + " in the admin console and run tw up again")
	}
	tn := lb.Tailnet
	if tn == "" {
		if _, suf, ok := strings.Cut(lb.DNSName, "."); ok {
			tn = suf
		}
	}
	if tailnet == "" {
		tailnet = tn
	} else if control == prodControl && tn != "" && tn != tailnet {
		return "", fail("state_conflict", "mgmt's node joined tailnet "+tn+", not --tailnet "+tailnet).
			withFix("drop --tailnet (it is discovered), or use the right credential")
	}
	if lb.Funnel {
		// mgmt never serves Funnel; a funnel attribute on its tag (from an
		// older tailworks' keyless snippet) is only exposure to remove.
		fmt.Fprintf(os.Stderr, "! policy   %s has the funnel attribute; tailworks doesn't use it: remove {\"target\": [\"%s\"], \"attr\": [\"funnel\"]} from nodeAttrs\n",
			mgmt.MgmtTag, mgmt.MgmtTag)
	}
	return tailnet, nil
}
