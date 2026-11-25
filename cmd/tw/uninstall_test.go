package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	twagent "github.com/rosskukulinski/tailworks/agent"
	"github.com/rosskukulinski/tailworks/internal/npminstall"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// uninstallEnv is a hermetic machine for tw uninstall: a temporary HOME
// (agents' skills, the login service's unit) and state dir, no terminal,
// no npm install, no setup record, and a fake credential that records the
// nodes it deletes. Nothing outside t.TempDir() is ever touched.
type uninstallEnv struct {
	home, state string
	prefix      string // the binary's install prefix ("" = none)
	npm         *npminstall.Install
	deleted     []string
	deleteErr   error
	setupRecord string
}

func newUninstallEnv(t *testing.T) *uninstallEnv {
	t.Helper()
	root := t.TempDir()
	e := &uninstallEnv{home: filepath.Join(root, "home"), state: filepath.Join(root, "home", ".tailworks")}
	os.MkdirAll(e.state, 0o700)
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TW_STATE_DIR", "")
	oldRec, oldCh, oldDel, oldEnv, oldTerm := uninstallSetupRecord, uninstallChannel, uninstallDeleteNode, uninstallAgentEnv, confirmIsTerminal
	t.Cleanup(func() {
		uninstallSetupRecord, uninstallChannel, uninstallDeleteNode, uninstallAgentEnv, confirmIsTerminal = oldRec, oldCh, oldDel, oldEnv, oldTerm
	})
	uninstallSetupRecord = func() string { return e.setupRecord }
	uninstallChannel = func() installInfo {
		if e.npm != nil {
			return installInfo{Channel: channelNPM, NPM: *e.npm}
		}
		if e.prefix != "" {
			return installInfo{Channel: channelPrefix, Prefix: e.prefix}
		}
		return installInfo{Channel: channelOther}
	}
	uninstallDeleteNode = func(_ context.Context, _ *state, id string) error {
		if e.deleteErr != nil {
			return e.deleteErr
		}
		e.deleted = append(e.deleted, id)
		return nil
	}
	uninstallAgentEnv = func() (twagent.Env, error) { return twagent.Env{Home: e.home}, nil } // no LookPath: only ~/.<agent> dirs count
	confirmIsTerminal = func() bool { return false }
	return e
}

func (e *uninstallEnv) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(e.state, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// install lays out an install.sh prefix in dir (the state dir by default).
func (e *uninstallEnv) install(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{"bin/tw", "bin.prev/tw", "share/tailworks/release.json", "share.prev/tailworks/release.json", "VERSION", "VERSION.prev", "install.sh", ".install-123/x"} {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("x"), 0o600)
	}
}

// configured writes a tw up state: oauth, single layout, mgmt's node ID.
func (e *uninstallEnv) configured(t *testing.T) {
	e.write(t, "tw.json", `{"v":1,"tailnet":"example.ts.net","tenantID":"t1","mintMode":"oauth","oauthClientFile":"/nonexistent/oauth.env",`+
		`"mgmtDNSName":"tw-mgmt.example.ts.net","mgmtNodeID":"nMGMT","hostID":"h1","mode":"single","controlURL":"https://controlplane.tailscale.com"}`)
	e.write(t, "mgmt/mgmt.db", "db")
	e.write(t, "hostd/objects.json", "{}")
	e.write(t, "logs/supervisor.log", "")
	e.write(t, "backups/20260101/mgmt.db", "db")
}

func (e *uninstallEnv) run(t *testing.T, args ...string) (uninstallResult, string, error) {
	t.Helper()
	var res uninstallResult
	out, err := captureStdout(t, func() error { return cmdUninstall(append([]string{"--state-dir", e.state}, args...)) })
	if err == nil && strings.Contains(strings.Join(args, " "), "--json") {
		if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
			t.Fatalf("--json: %v\n%s", jerr, out)
		}
	}
	return res, out, err
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// Without --yes and without a terminal it refuses, and nothing changes;
// --dry-run prints the plan and changes nothing either.
func TestUninstallAsksFirst(t *testing.T) {
	e := newUninstallEnv(t)
	e.install(t, e.state)
	e.write(t, "backups/b1/tw.json", "{}")
	_, out, err := e.run(t)
	if cliCode(err) != "confirmation_required" {
		t.Fatalf("no --yes: %v, want confirmation_required", err)
	}
	if !strings.Contains(cliText(err), "--yes") || !strings.Contains(cliText(err), "backups/") || !strings.Contains(cliText(err), "tw backup --out") {
		t.Errorf("refusal: %s", cliText(err))
	}
	for _, p := range []string{"bin", "share", "VERSION", "backups"} {
		if !strings.Contains(out, filepath.Join(e.state, p)) {
			t.Errorf("the plan doesn't list %s:\n%s", p, out)
		}
		if !exists(filepath.Join(e.state, p)) {
			t.Errorf("%s deleted without --yes", p)
		}
	}
	res, _, err := e.run(t, "--dry-run", "--json")
	if err != nil || !res.DryRun || res.Done || len(res.Remove) == 0 {
		t.Fatalf("--dry-run: %+v %v", res, err)
	}
	if !exists(filepath.Join(e.state, "bin")) {
		t.Error("--dry-run deleted something")
	}
}

// A configured state that isn't running: refused (the purge needs mgmt)
// unless --force; --force removes it all, deletes mgmt's node with the
// tenant's credential and leaves what isn't tailworks'.
func TestUninstallConfiguredForce(t *testing.T) {
	e := newUninstallEnv(t)
	e.install(t, e.state)
	e.configured(t)
	e.write(t, "notes.txt", "mine")
	if _, _, err := e.run(t, "--yes"); cliCode(err) != "uninstall_blocked" || !strings.Contains(cliText(err), "--force") {
		t.Fatalf("not running, no --force: %v", err)
	}
	if !exists(filepath.Join(e.state, "tw.json")) {
		t.Fatal("refused, but deleted tw.json")
	}
	res, _, err := e.run(t, "--yes", "--force", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || strings.Join(e.deleted, ",") != "nMGMT" {
		t.Fatalf("done %v, nodes deleted %v", res.Done, e.deleted)
	}
	for _, p := range append(append([]string{}, stateEntries...), prefixEntries...) {
		if exists(filepath.Join(e.state, p)) {
			t.Errorf("%s is left", p)
		}
	}
	if exists(filepath.Join(e.state, ".install-123")) {
		t.Error(".install-* is left")
	}
	if b, err := os.ReadFile(filepath.Join(e.state, "notes.txt")); err != nil || string(b) != "mine" {
		t.Errorf("a file that isn't tailworks' was deleted: %v", err)
	}
	if !strings.Contains(strings.Join(res.Kept, "\n"), "notes.txt (not tailworks')") {
		t.Errorf("kept: %v", res.Kept)
	}
	man := strings.Join(res.Manual, "\n")
	for _, want := range []string{"admin console › Machines", "revoke its OAuth client", "tailnet policy"} {
		if !strings.Contains(man, want) {
			t.Errorf("manual steps don't say %q:\n%s", want, man)
		}
	}
	// With nothing else in it, the state dir goes too.
	os.Remove(filepath.Join(e.state, "notes.txt"))
	e.install(t, e.state)
	if _, _, err := e.run(t, "--yes"); err != nil {
		t.Fatal(err)
	}
	if exists(e.state) {
		t.Errorf("%s is left", e.state)
	}
}

// --keep-state: the install's own files leave the state dir (it is the
// prefix), the state stays, and tw.json forgets mgmt's deleted node.
func TestUninstallKeepState(t *testing.T) {
	e := newUninstallEnv(t)
	e.install(t, e.state)
	e.configured(t)
	res, _, err := e.run(t, "--keep-state", "--force", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"bin", "share", "VERSION", "install.sh", "bin.prev"} {
		if exists(filepath.Join(e.state, p)) {
			t.Errorf("%s is left", p)
		}
	}
	for _, p := range []string{"tw.json", "mgmt/mgmt.db", "hostd/objects.json", "backups/20260101/mgmt.db", "logs"} {
		if !exists(filepath.Join(e.state, p)) {
			t.Errorf("--keep-state deleted %s", p)
		}
	}
	if !res.KeepState || len(res.Kept) == 0 {
		t.Errorf("result: %+v", res)
	}
	s, err := openState(e.state)
	if err != nil || s.Cfg.MgmtNodeID != "" || s.Cfg.TenantID != "t1" {
		t.Errorf("tw.json after --keep-state: %+v %v", s.Cfg, err)
	}
}

// A credential that may not delete devices: the rest still happens, and
// the admin console line says what's left.
func TestUninstallNodeDeleteForbidden(t *testing.T) {
	e := newUninstallEnv(t)
	e.configured(t)
	e.deleteErr = &tsapi.APIError{Status: http.StatusForbidden, Message: "forbidden"}
	res, _, err := e.run(t, "--force", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Manual, "\n"), "delete mgmt's node tw-mgmt.example.ts.net (nMGMT) in the admin console › Machines") {
		t.Errorf("manual: %v", res.Manual)
	}
	if exists(filepath.Join(e.state, "tw.json")) {
		t.Error("tw.json is left")
	}
}

// A linked developer machine: link.json and cli/ go; nothing to stop.
func TestUninstallLinkOnly(t *testing.T) {
	e := newUninstallEnv(t)
	e.write(t, "link.json", `{"endpoint":"https://tw-mgmt.example.ts.net"}`)
	e.write(t, "cli/tailscaled.state", "{}")
	e.write(t, "update-check.json", "{}")
	res, _, err := e.run(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if exists(e.state) {
		t.Errorf("left %s", e.state)
	}
	if !strings.Contains(strings.Join(res.Steps, "\n"), "forget the link") {
		t.Errorf("steps: %v", res.Steps)
	}
}

// A prefix apart from the state dir (TW_HOME): its files and the empty
// prefix go; with --keep-state the state dir is untouched.
func TestUninstallSeparatePrefix(t *testing.T) {
	e := newUninstallEnv(t)
	e.prefix = filepath.Join(filepath.Dir(e.home), "opt-tw")
	e.install(t, e.prefix)
	e.write(t, "backups/b1/tw.json", "{}")
	res, _, err := e.run(t, "--keep-state", "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if exists(e.prefix) {
		t.Errorf("prefix %s is left", e.prefix)
	}
	if res.Prefix != e.prefix || !exists(filepath.Join(e.state, "backups/b1/tw.json")) {
		t.Errorf("prefix %q; backups kept: %v", res.Prefix, exists(filepath.Join(e.state, "backups")))
	}
	if !strings.Contains(strings.Join(res.Manual, "\n"), "PATH line for "+filepath.Join(e.prefix, "bin")) {
		t.Errorf("manual: %v", res.Manual)
	}
}

// tailworks' skills are removed from the agents found here, and only
// those files.
func TestUninstallRemovesSkills(t *testing.T) {
	e := newUninstallEnv(t)
	os.MkdirAll(filepath.Join(e.home, ".claude"), 0o700)
	env := twagent.Env{Home: e.home}
	a, _ := twagent.AgentByID("claude")
	plan, err := twagent.MakePlan(env, twagent.Options{Version: version, Scope: twagent.ScopeUser, Agents: []twagent.Agent{a}})
	if err != nil {
		t.Fatal(err)
	}
	if err := twagent.Apply(plan); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(e.home, ".claude", "settings.json")
	os.WriteFile(mine, []byte("{}"), 0o600)
	res, _, err := e.run(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) == 0 {
		t.Fatalf("no skills in the plan: %+v", res)
	}
	for _, c := range plan.Changes {
		if c.Action == twagent.Create && c.Kind != "block" && exists(c.Path) {
			t.Errorf("skill file %s is left", c.Path)
		}
	}
	if !exists(mine) {
		t.Error("deleted a file tailworks didn't write")
	}
}

// The refusals that come before anything else.
func TestUninstallGuards(t *testing.T) {
	e := newUninstallEnv(t)
	e.install(t, e.state)
	e.setupRecord = filepath.Join(e.home, "setup.json")
	os.WriteFile(e.setupRecord, []byte("{}"), 0o600)
	e.prefix = e.state
	_, _, err := e.run(t, "--yes", "--force")
	if cliCode(err) != "uninstall_blocked" || !strings.Contains(cliText(err), "sudo "+filepath.Join(e.state, "bin", "tw")+" host setup --undo") {
		t.Errorf("setup record: %v", err)
	}
	e.setupRecord = ""
	if cliCode(uninstallSafeDir("/")) != "uninstall_blocked" {
		t.Error("/ is not refused")
	}
	for _, dir := range []string{e.home, filepath.Dir(e.home)} {
		err := cmdUninstall([]string{"--state-dir", dir, "--yes", "--force"})
		if cliCode(err) != "uninstall_blocked" {
			t.Errorf("--state-dir %s: %v, want uninstall_blocked", dir, err)
		}
	}
	if !exists(filepath.Join(e.state, "bin")) {
		t.Error("a refusal deleted something")
	}
}

// Installed with npm: the package stays (npm owns it) and the npm command
// is printed; the state dir still goes.
func TestUninstallNPM(t *testing.T) {
	e := newUninstallEnv(t)
	e.npm = &npminstall.Install{Dir: filepath.Join(e.home, "lib/node_modules/@tailworks/cli-x"), Package: "@tailworks/cli-x"}
	e.write(t, "update-check.json", "{}")
	res, _, err := e.run(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if res.Prefix != "" || !strings.Contains(strings.Join(res.Manual, "\n"), "npm uninstall -g tailworks") {
		t.Errorf("prefix %q, manual %v", res.Prefix, res.Manual)
	}
	if exists(e.state) {
		t.Errorf("left %s", e.state)
	}
}
