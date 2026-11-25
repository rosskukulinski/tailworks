//go:build e2etwupgrade

// Package twupgrade is the end-to-end test of single-machine `tw upgrade`
// (issue #111; ADR-041 §8): backup, stop, install.sh, start, the health
// gate, and automatic rollback, driven only through the tw CLI of a real
// install.sh prefix, on testcontrol with a fake Tailscale API (hermetic).
//
// Release N is this tree stamped 0.0.1; N+1 is this tree stamped VERSION.
// Both are signed with a key made for the run, pinned with
// TW_RELEASE_SIGNERS (for tw upgrade and install.sh alike). The test:
//
//  1. installs N with its install.sh and runs `tw up` (single process) from
//     the prefix, the state dir being the prefix as with get.sh;
//
//  2. deploys an app and writes data to its Workers KV namespace and its
//     D1 database;
//
//  3. offers tw upgrade an unsigned N+1, one signed by an unpinned key and
//     a tampered one, and checks each is refused before anything in it
//     runs (its install.sh would leave a file behind) and nothing changed;
//
//  4. upgrades to an N+1 whose tw doesn't start, and to an N+1 that starts
//     but never reports the version it claims: the health gate fails, and
//     tw upgrade rolls back to N (install.sh --rollback, the store from
//     its backup), which runs again with the data;
//
//  5. upgrades to a good N+1: a backup is taken, tw restarts on N+1, the
//     health gate passes, and the data is there;
//
//  6. restarts N+1 (tw down, tw up);
//
//  7. rolls back to N with tw upgrade rollback (binaries only: no contract
//     migration ran), and the data is there.
//
// Run it with `make e2e-tw-upgrade`.
package twupgrade

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/releasesig"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const oldV = "0.0.1"

type machine struct {
	t      *testing.T
	prefix string // the install.sh prefix, also the state dir
	app    string
	env    []string
}

// tw runs the installed tw (prefix/bin/tw) in dir (the app's when "").
func (m *machine) tw(dir string, args ...string) (string, string, error) {
	cmd := exec.Command(filepath.Join(m.prefix, "bin", "tw"), append([]string{"--state-dir", m.prefix}, args...)...)
	cmd.Dir = cmpOr(dir, m.app)
	cmd.Env = m.env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

func (m *machine) must(args ...string) string {
	m.t.Helper()
	so, se, err := m.tw("", args...)
	if err != nil {
		m.t.Fatalf("tw %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, so, netd.Redact(se))
	}
	return so
}

func (m *machine) mustJSON(out any, args ...string) {
	m.t.Helper()
	so := m.must(append(args, "--json")...)
	if err := json.Unmarshal([]byte(so), out); err != nil {
		m.t.Fatalf("tw %s: bad JSON: %v\n%s", strings.Join(args, " "), err, so)
	}
}

func (m *machine) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(m.prefix, name))
	return strings.TrimSpace(string(b))
}

func (m *machine) supervisorPID() int {
	pid, _ := strconv.Atoi(m.read("supervisor.pid"))
	return pid
}

func (m *machine) backups() []string {
	es, _ := os.ReadDir(filepath.Join(m.prefix, "backups"))
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// installedVersion is what prefix/bin/tw says it is.
func (m *machine) installedVersion() string {
	var v struct{ Tw string }
	m.mustJSON(&v, "version")
	return v.Tw
}

// runningVersion is the version the running tw up's mgmt reports
// (GET /v1/status, through `tw version --check`).
func (m *machine) runningVersion() string {
	m.t.Helper()
	var v struct {
		Daemons *struct{ Version string }
	}
	m.mustJSON(&v, "version", "--check")
	if v.Daemons == nil {
		m.t.Fatalf("tw version --check: no running tw up")
	}
	return v.Daemons.Version
}

// checkData asserts the app's deployment is active and its KV and D1 data
// are what was written before the first upgrade.
func (m *machine) checkData(when string) {
	m.t.Helper()
	var ls struct {
		Deployments []struct{ Workload, Status string }
	}
	// A rollback returns once tw up is back, which can be a moment before
	// the host has redeployed the app ("starting"): wait for it.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		m.mustJSON(&ls, "ls")
		active, starting := false, false
		for _, d := range ls.Deployments {
			active = active || d.Workload == "upgapp" && d.Status == "active"
			starting = starting || d.Workload == "upgapp" && d.Status == "starting"
		}
		if active {
			break
		}
		if !starting || time.Now().After(deadline) {
			m.t.Fatalf("%s: upgapp is not active: %+v", when, ls.Deployments)
		}
	}
	if got := m.must("kv", "key", "get", "greeting", "--binding", "CACHE"); got != "hello from "+oldV {
		m.t.Fatalf("%s: KV greeting = %q", when, got)
	}
	var d1 struct {
		Results []struct {
			Results struct{ Rows [][]any }
		}
	}
	// D1 queries reach the running workload (through mgmt to the host).
	deadline := time.Now().Add(60 * time.Second)
	for {
		so, se, err := m.tw("", "d1", "execute", "upg-db", "--command", "SELECT body FROM notes ORDER BY id", "--json")
		if err == nil {
			if err := json.Unmarshal([]byte(so), &d1); err != nil {
				m.t.Fatalf("%s: d1 execute: %v\n%s", when, err, so)
			}
			break
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("%s: d1 execute: %v\n%s\n%s", when, err, so, se)
		}
		time.Sleep(time.Second)
	}
	if len(d1.Results) != 1 || len(d1.Results[0].Results.Rows) != 2 ||
		d1.Results[0].Results.Rows[0][0] != "written on "+oldV || d1.Results[0].Results.Rows[1][0] != "second row" {
		m.t.Fatalf("%s: D1 rows: %+v", when, d1.Results)
	}
}

// upgradeResult is tw upgrade --json's result (cmd/tw localResult).
type upgradeResult struct {
	Plan struct {
		From, To string
		Steps    []string
	}
	Upgraded   bool
	Backup     string
	RolledBack bool
	Health     string

	AgentSkills string // issue #490
}

func TestTwUpgradeSingleMachine(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	root, err := nettest.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	vb, _ := os.ReadFile(filepath.Join(root, "VERSION"))
	newV := strings.TrimSpace(string(vb))

	// The run's release key, and one tw doesn't trust.
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, rogue, _ := ed25519.GenerateKey(rand.Reader)
	work := t.TempDir()
	signers := filepath.Join(work, "allowed_signers")
	if err := os.WriteFile(signers, []byte(releasesig.AuthorizedKey(pub, "e2e-twupgrade")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldBin, newBin := buildBins(t, root, oldV), buildBins(t, root, newV)
	rel := func(name string, r release) string { return r.build(t, root, filepath.Join(work, "releases", name)) }
	sentinel := filepath.Join(work, "refused-release-ran")
	relN := rel("n", release{Version: oldV, Bins: oldBin, Key: key})
	relGood := rel("good", release{Version: newV, Bins: newBin, Key: key})
	relNoStart := rel("nostart", release{Version: newV, Bins: newBin, Key: key, BrokenUp: true})
	relWrongV := rel("wrongversion", release{Version: newV, Bins: oldBin, Key: key}) // claims N+1, runs N
	relUnsigned := rel("unsigned", release{Version: newV, Bins: newBin, Sentinel: sentinel})
	relRogue := rel("rogue", release{Version: newV, Bins: newBin, Key: rogue, Sentinel: sentinel})
	relTampered := rel("tampered", release{Version: newV, Bins: newBin, Key: key, Sentinel: sentinel, TamperAfterSigning: true})

	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	api := newFakeAPI(t, "v"+newV)
	prefix, err := hostdtest.ShortDir("tw-tu-")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(work, "upgapp")
	if err := os.CopyFS(app, os.DirFS(filepath.Join(root, "e2e", "twupgrade", "testdata", "app"))); err != nil {
		t.Fatal(err)
	}
	m := &machine{t: t, prefix: prefix, app: app}
	// A home of its own, with a skill an older tw agent setup wrote: the
	// good upgrade refreshes it (issue #490), and the real home isn't
	// touched.
	home := filepath.Join(work, "home")
	skill := filepath.Join(home, ".claude", "skills", "tw", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("<!-- tw agent skills 0.0.1 -->\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.env = append(os.Environ(),
		"HOME="+home,
		"TW_RELEASE_SIGNERS="+signers,
		"TW_WORKERD="+wd,
		"TW_CREATE_TAILWORKS="+filepath.Join(root, "js", "packages", "create-tailworks"),
		"TW_NO_UPDATE_CHECK=1",
		"TW_RELEASE_API="+api.URL,
		"TW_ALLOW_UNSIGNED=", // the signature checks are under test
	)
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(prefix, "logs", c+".log"))
				t.Logf("---- %s.log (tail)\n%s", c, netd.Redact(string(b[max(0, len(b)-6000):])))
			}
		}
		if os.Getenv("TW_KEEP") != "" {
			t.Logf("TW_KEEP: left %s", prefix)
			return
		}
		m.tw(work, "down", "--purge")
		os.RemoveAll(prefix)
	})

	// 1. Release N, installed by its install.sh (signature checked with the
	// run's key), then tw up from the prefix.
	stageN := filepath.Join(t.TempDir(), "n")
	if err := releasesig.ExtractTarGz(relN, stageN); err != nil {
		t.Fatal(err)
	}
	inst := exec.Command("sh", filepath.Join(stageN, filepath.Base(strings.TrimSuffix(relN, ".tar.gz")), "install.sh"), "--prefix", prefix)
	inst.Env = m.env
	if out, err := inst.CombinedOutput(); err != nil || !strings.Contains(string(out), "✓ signed by a tw release key") {
		t.Fatalf("install.sh (N): %v\n%s", err, out)
	}
	if v := m.installedVersion(); v != oldV {
		t.Fatalf("installed tw is %q, want %s", v, oldV)
	}
	keyFile := cliflow.WriteFakeOAuthFile(t, filepath.Join(work, "oauth.env"))
	m.must("up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--control-url", ctl.URL, "--tsapi-url", api.URL,
		"--hostname-prefix", "tw-tu-", "--mgmt-hostname", "tw-tu-mgmt", "--host-id", "tw-tu",
		"--worker-internet=off") // kept by every restart below (#295)
	if v := m.runningVersion(); v != oldV {
		t.Fatalf("running tw up is %q, want %s", v, oldV)
	}

	// 2. An app with data in KV (mgmt's store) and D1 (the workload's).
	var dep struct{ Status string }
	m.mustJSON(&dep, "deploy", app)
	if dep.Status != "active" {
		t.Fatalf("deploy: %+v", dep)
	}
	m.must("kv", "namespace", "create", "cache")
	m.must("kv", "key", "put", "greeting", "hello from "+oldV, "--binding", "CACHE")
	m.must("d1", "execute", "upg-db", "--command",
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes (body) VALUES ('written on "+oldV+"'), ('second row')")
	m.checkData("before the upgrades")
	m.checkWorkerInternetOff("before the upgrades")

	// The plan changes nothing.
	var plan upgradeResult
	m.mustJSON(&plan, "upgrade", "--from", relGood, "--plan")
	if plan.Plan.From != oldV || plan.Plan.To != newV || len(plan.Plan.Steps) != 6 || plan.Upgraded || plan.Backup != "" ||
		!strings.Contains(plan.Plan.Steps[5], "agent skills") {
		t.Fatalf("plan: %+v", plan)
	}

	// 3. Refused before anything in the release runs.
	pid0 := m.supervisorPID()
	for _, c := range []struct{ name, tgz, code string }{
		{"unsigned", relUnsigned, "release_unsigned"},
		{"signed by an unpinned key", relRogue, "release_bad_signature"},
		{"tampered after signing", relTampered, "release_bad_signature"},
	} {
		so, se, err := m.tw("", "upgrade", "--from", c.tgz, "--yes", "--json")
		if err == nil || !strings.Contains(se, c.code) {
			t.Fatalf("%s release: want %s, got %v\nstdout: %s\nstderr: %s", c.name, c.code, err, so, se)
		}
		if _, err := os.Stat(sentinel); err == nil {
			t.Fatalf("%s release: its install.sh ran", c.name)
		}
		if v := m.read("VERSION"); v != oldV || len(m.backups()) != 0 || m.supervisorPID() != pid0 {
			t.Fatalf("%s release: something changed: VERSION %s, backups %v, supervisor %d (was %d)", c.name, v, m.backups(), m.supervisorPID(), pid0)
		}
		t.Logf("✓ %s release refused: %s", c.name, firstLine(se))
	}

	// 4. N+1 fails the health gate: rolled back to N with the data.
	for i, c := range []struct{ name, tgz, health string }{
		{"doesn't start", relNoStart, "mgmt"}, // mgmt never answers
		{"runs the wrong version", relWrongV, "runs " + oldV},
	} {
		pid := m.supervisorPID()
		st := time.Now()
		so, se, err := m.tw("", "upgrade", "--from", c.tgz, "--yes", "--json", "--health-timeout", "20s")
		var res upgradeResult
		json.Unmarshal([]byte(so), &res)
		if err == nil || !strings.Contains(se, "upgrade_failed") || !res.RolledBack || !strings.Contains(res.Health, c.health) {
			t.Fatalf("N+1 that %s: want a rollback after a %q health failure, got %v\nstdout: %s\nstderr: %s", c.name, c.health, err, so, se)
		}
		if bs := m.backups(); len(bs) != i+1 || res.Backup != filepath.Join(prefix, "backups", bs[len(bs)-1]) {
			t.Fatalf("N+1 that %s: backups %v, result says %q", c.name, bs, res.Backup)
		}
		if _, err := os.Stat(filepath.Join(res.Backup, "mgmt", "mgmt.db")); err != nil {
			t.Fatalf("N+1 that %s: backup has no store: %v", c.name, err)
		}
		if v, pv := m.read("VERSION"), m.read("VERSION.prev"); v != oldV || pv != newV {
			t.Fatalf("N+1 that %s: after the rollback VERSION %s, VERSION.prev %s", c.name, v, pv)
		}
		if p := m.supervisorPID(); p == 0 || p == pid {
			t.Fatalf("N+1 that %s: supervisor %d (was %d): tw was not restarted", c.name, p, pid)
		}
		if v := m.installedVersion(); v != oldV {
			t.Fatalf("N+1 that %s: installed tw is %s after the rollback", c.name, v)
		}
		if v := m.runningVersion(); v != oldV {
			t.Fatalf("N+1 that %s: running tw up is %s after the rollback", c.name, v)
		}
		m.checkData("after rolling back an N+1 that " + c.name)
		m.checkWorkerInternetOff("after rolling back an N+1 that " + c.name)
		t.Logf("✓ N+1 that %s: rolled back to %s in %v (health: %s)", c.name, oldV, time.Since(st).Round(time.Second), res.Health)
	}

	// 5. A good N+1.
	pid := m.supervisorPID()
	st := time.Now()
	var res upgradeResult
	m.mustJSON(&res, "upgrade", "--from", relGood, "--yes", "--health-timeout", "3m")
	if !res.Upgraded || res.Health != "ok" || res.RolledBack || !strings.Contains(res.AgentSkills, newV) {
		t.Fatalf("upgrade to the good N+1: %+v", res)
	}
	if b, _ := os.ReadFile(skill); !strings.Contains(string(b), "<!-- tw agent skills "+newV) {
		t.Fatalf("the agent skill wasn't refreshed by the new tw: %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "tw-deploy")); err == nil {
		t.Fatal("the refresh created a skill tw hadn't written")
	}
	if bs := m.backups(); len(bs) != 3 || !slices.Contains(bs, filepath.Base(res.Backup)) || !strings.HasSuffix(res.Backup, "-"+oldV) {
		t.Fatalf("backups %v, result says %q", bs, res.Backup)
	}
	for _, f := range []string{"mgmt/mgmt.db", "tw.json"} {
		if _, err := os.Stat(filepath.Join(res.Backup, f)); err != nil {
			t.Fatalf("backup: %v", err)
		}
	}
	if v, pv := m.read("VERSION"), m.read("VERSION.prev"); v != newV || pv != oldV {
		t.Fatalf("after the upgrade VERSION %s, VERSION.prev %s", v, pv)
	}
	if p := m.supervisorPID(); p == 0 || p == pid {
		t.Fatalf("supervisor %d (was %d): tw was not restarted", p, pid)
	}
	if v := m.installedVersion(); v != newV {
		t.Fatalf("installed tw is %s after the upgrade", v)
	}
	if v := m.runningVersion(); v != newV {
		t.Fatalf("running tw up is %s after the upgrade", v)
	}
	m.checkData("after the upgrade")
	m.checkWorkerInternetOff("after the upgrade")
	t.Logf("✓ upgraded %s → %s in %v, backup %s", oldV, newV, time.Since(st).Round(time.Second), res.Backup)
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("a refused release's install.sh ran")
	}

	// 6. A plain restart (a reboot, a service restart) keeps it too.
	m.must("down")
	m.must("up", "--yes")
	m.checkWorkerInternetOff("after tw down and tw up")

	// 7. tw upgrade rollback (issue #486) puts N back by hand. No contract
	// migration ran between N and N+1, so the store stays.
	st = time.Now()
	var rb struct {
		From, To, Mode, SafetyBackup, Health string
	}
	m.mustJSON(&rb, "upgrade", "rollback", "--yes")
	if rb.From != newV || rb.To != oldV || rb.Mode != "binary" || rb.Health != "ok" || !strings.HasSuffix(rb.SafetyBackup, "-before-rollback") {
		t.Fatalf("tw upgrade rollback: %+v", rb)
	}
	if _, err := os.Stat(filepath.Join(rb.SafetyBackup, "mgmt", "mgmt.db")); err != nil {
		t.Fatalf("the state before the rollback wasn't kept: %v", err)
	}
	if v, pv := m.read("VERSION"), m.read("VERSION.prev"); v != oldV || pv != newV {
		t.Fatalf("after tw upgrade rollback VERSION %s, VERSION.prev %s", v, pv)
	}
	if v := m.installedVersion(); v != oldV {
		t.Fatalf("installed tw is %s after tw upgrade rollback", v)
	}
	if v := m.runningVersion(); v != oldV {
		t.Fatalf("running tw up is %s after tw upgrade rollback", v)
	}
	m.checkData("after tw upgrade rollback")
	m.checkWorkerInternetOff("after tw upgrade rollback")
	t.Logf("✓ tw upgrade rollback %s → %s in %v", newV, oldV, time.Since(st).Round(time.Second))
}

// checkWorkerInternetOff asserts tw up's --worker-internet=off survived:
// tw.json still says off, and the running host reports off (tw status
// --json fails when the host isn't up, and its workerInternet is what
// the host reports).
func (m *machine) checkWorkerInternetOff(when string) {
	m.t.Helper()
	var cfg struct{ WorkerInternet string }
	if err := json.Unmarshal([]byte(m.read("tw.json")), &cfg); err != nil || cfg.WorkerInternet != "off" {
		m.t.Fatalf("%s: tw.json workerInternet = %q (%v), want off", when, cfg.WorkerInternet, err)
	}
	var st struct {
		Host           bool
		WorkerInternet string
	}
	m.mustJSON(&st, "status")
	if !st.Host || st.WorkerInternet != "off" {
		m.t.Fatalf("%s: tw status: host %v, workerInternet %q, want off", when, st.Host, st.WorkerInternet)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
