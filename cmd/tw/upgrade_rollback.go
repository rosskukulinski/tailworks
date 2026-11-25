package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	twversion "github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw upgrade rollback (issue #486): put the release install.sh kept as
// bin.prev / share.prev / VERSION.prev back, the same steps tw upgrade's
// automatic rollback takes (rollback, upgrade.go), as one command.
//
//   - Binary only, when the store says the previous tw-mgmt can still open
//     it (no contract migration since: mgmt.CheckSchema's MinBinary).
//   - Otherwise (or with --restore, or a backup dir), the store goes back
//     too: the newest <state>/backups/<time>-<VERSION.prev> that tw upgrade
//     took before it installed, then the restore fence, as tw restore does.
//
// Either way the state it replaces is kept first, in
// <state>/backups/<time>-before-rollback.

const upgradeRollbackHealthTimeout = 3 * time.Minute

// upgradeRollbackResult is `tw upgrade rollback --json`.
type upgradeRollbackResult struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Prefix string `json:"prefix"`
	// Mode is "binary" (the store stays) or "restore" (it went back to
	// Restored).
	Mode              string `json:"mode"`
	Restored          string `json:"restored,omitempty"`
	Store             string `json:"store,omitempty"`
	SafetyBackup      string `json:"safetyBackup,omitempty"`
	RestoreGeneration int64  `json:"restoreGeneration,omitempty"`
	Health            string `json:"health,omitempty"`
	// Next is what is left to do by hand (a Postgres store's restore).
	Next []string `json:"next,omitempty"`
}

func cmdUpgradeRollback(args []string) error {
	f := newFlags("upgrade rollback")
	restore := f.Bool("restore", false, "also put the store back from the backup tw upgrade took (default: only when the previous release can't open the store)")
	prefixFlag := f.String("prefix", "", "the install.sh prefix to roll back (default: the one that owns --state-dir)")
	yes := yesFlagBefore(f, "rolling back")
	f.group(advancedFlags, "prefix")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw upgrade rollback takes at most one backup directory", "usage: tw "+upgradeUsage)
	}
	backupArg := ""
	if len(pos) == 1 {
		backupArg = pos[0]
	}
	// Restoring the store is destructive: asked before anything else when
	// it was asked for, later when the store's schema calls for it.
	confirmed := false
	if *restore || backupArg != "" {
		if err := upgradeRollbackConfirmRestore(f, *yes, cmpOr(backupArg, "the backup tw upgrade took")); err != nil {
			return err
		}
		confirmed = true
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if s.remote() {
		return fail("remote_unsupported", "tw upgrade rollback runs on the machine that runs tw up (this machine is linked to "+s.Link.Endpoint+")").
			withFix("run it there; a fleet goes back with another upgrade run: tw upgrade fleet <version>")
	}
	pc, err := resolvePrefix(*prefixFlag, installPrefixOf(), statePrefixCandidates(s)) // upgrade_prefix.go
	if err != nil {
		return err
	}
	prefix := pc.Prefix
	if in, npm := npmSelf(); npm && prefix == "" {
		return fail("npm_install", "this tailworks was installed by "+in.Kind()+" ("+in.Package+" "+in.Version+"): npm keeps no previous release, so there is nothing for tw upgrade rollback to put back").
			withFix("tw down; npm install -g tailworks@<previous>; tw up",
				"if a migration ran since: tw restore <backup> --yes before tw up")
	}
	if prefix == "" {
		return fail("bad_request", "this tailworks doesn't run from an install.sh prefix (bin/, share/tailworks, VERSION), so there is no previous release to roll back to").
			withFix("name the install: tw upgrade rollback --prefix <dir>")
	}
	if pc.Mismatch != "" {
		return fail("prefix_mismatch", pc.Mismatch+": tw upgrade rollback won't guess which one to roll back").
			withFix("tw upgrade rollback --prefix "+prefix+"   (the install that runs the state dir)",
				"or run the tailworks installed there: "+filepath.Join(prefix, "bin", "tw")+" upgrade rollback")
	}
	for _, p := range []string{"bin.prev", "share.prev", "VERSION.prev"} {
		if !fileExists(filepath.Join(prefix, p)) {
			return fail("bad_request", "no previous release in "+prefix+" (bin.prev, share.prev and VERSION.prev; install.sh keeps them on an upgrade): nothing to roll back to").
				withFix("install the older release by hand: tw down; ./tailworks-<v>-<os>-<arch>/install.sh --prefix " + prefix + " --force; tw restore <backup> --yes; tw up")
		}
	}
	cur := installedVersion(prefix) // upgrade.go
	prev := upgradeRollbackRead(filepath.Join(prefix, "VERSION.prev"))
	if pv, err := twversion.Parse(prev); err == nil {
		if cv, err := twversion.Parse(cur); err == nil && pv.Compare(cv) > 0 {
			return fail("bad_request", fmt.Sprintf("the previous install in %s is tw %s, newer than the installed %s (it was rolled back already): nothing to roll back to", prefix, prev, cur)).
				withFix("go forward again: tw upgrade "+prev,
					"or swap them back as they were: tw down; "+filepath.Join(prefix, "install.sh")+" --rollback; tw up")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res := upgradeRollbackResult{From: cur, To: prev, Prefix: prefix, Mode: "binary"}
	backup := ""
	if *restore || backupArg != "" {
		res.Mode = "restore"
	} else if why, err := upgradeRollbackNeedsRestore(ctx, s, prev); err != nil {
		return err
	} else if why != "" {
		res.Mode = "restore"
		if !*f.json {
			fmt.Println("! " + why + ": the store goes back to the backup taken before the upgrade")
		}
	}
	if res.Mode == "restore" {
		if backup, err = upgradeRollbackBackup(s, backupArg, prev); err != nil {
			return err
		}
		res.Restored = backup
		if !confirmed {
			if err := upgradeRollbackConfirmRestore(f, *yes, backup); err != nil {
				return err
			}
		}
	} else if err := upgradeRollbackAsk(f, *yes, cur, prev); err != nil {
		return err
	}

	say := func(format string, a ...any) {
		if !*f.json {
			fmt.Printf(format+"\n", a...)
		}
	}
	wasService, wasRunning := restartMode(s) // upgrade.go
	var prior deploymentsSnapshot
	if wasService || wasRunning {
		prior = snapshotDeployments(s)
	}
	if err := stopTw(s, prefix, wasService, wasRunning); err != nil {
		return err
	}
	// 1. What is replaced is kept, so the rollback itself can be undone.
	if fileExists(filepath.Join(s.Dir, "mgmt")) {
		safety := filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-before-rollback")
		if _, err := backupMgmtTo(ctx, s, safety); err != nil {
			startTw(s, prefix, wasService, wasRunning) // nothing changed yet
			return fail("upgrade_failed", "could not back up the current state first: "+err.Error()).
				withFix("move " + filepath.Join(s.Dir, "mgmt") + " aside yourself, then tw upgrade rollback again")
		}
		res.SafetyBackup = safety
		say("✓ kept     %s (the state before the rollback)", safety)
	}
	// 2. The store, when it goes back.
	if res.Mode == "restore" {
		store, gen, err := upgradeRollbackRestore(ctx, s, backup, res.SafetyBackup)
		if err != nil {
			return fail("upgrade_failed", "restore "+backup+": "+err.Error()).
				withFix("tw restore " + res.SafetyBackup + " --yes   (puts the state from before back), then tw up")
		}
		res.Store, res.RestoreGeneration = store, gen
		say("✓ restore  %s", backup)
	}
	// 3. The previous release.
	if out, err := exec.CommandContext(ctx, "sh", filepath.Join(prefix, "install.sh"), "--prefix", prefix, "--rollback", "--force").CombinedOutput(); err != nil {
		return fail("upgrade_failed", "install.sh --rollback: "+err.Error(), lastLinesOf(string(out), 5)...).
			withFix("tw restore "+res.SafetyBackup+" --yes   (if the store went back), then tw up", "tw doctor")
	}
	say("✓ installed tw %s (tw %s kept as %s)", prev, cur, filepath.Join(prefix, "bin.prev"))
	if res.Store == "postgres" {
		res.Next = []string{
			"pg_restore --clean --if-exists -d <dsn> <the pg_dump taken before the upgrade>",
			"tw-mgmt restore-fence -db-file <file with the DSN>",
			"tw up",
		}
		emit(f, res, func() {
			fmt.Println("  the store is Postgres: restore it from the pg_dump taken before the upgrade, then fence it before tw up:")
			for _, l := range res.Next {
				fmt.Println("    " + l)
			}
		})
		return nil
	}
	// 4. Start it again the way it ran, and wait until it is healthy.
	if wasService || wasRunning {
		if err := startTw(s, prefix, wasService, wasRunning); err != nil {
			return fail("upgrade_failed", "rolled back to tw "+prev+", but it didn't start: "+err.Error()).
				withFix("tw logs --component mgmt   and   tw doctor", "the state before the rollback: "+res.SafetyBackup)
		}
		if err := waitHealthy(s, prev, upgradeRollbackHealthTimeout, prior); err != nil {
			return fail("upgrade_failed", fmt.Sprintf("rolled back to tw %s, but it did not become healthy (%v)", prev, err)).
				withFix("tw logs --component mgmt   and   tw doctor", "the state before the rollback: "+res.SafetyBackup)
		}
		res.Health = "ok"
	}
	emit(f, res, func() {
		if res.Health == "ok" {
			fmt.Printf("✓ tw %s is up and healthy (rolled back from %s)\n", prev, cur)
		} else {
			fmt.Printf("✓ rolled back to tw %s; tailworks wasn't running: tw up starts it\n", prev)
		}
		if res.Mode == "restore" {
			fmt.Printf("  the store is as of %s (lease epochs fenced, restore generation %d)\n", filepath.Base(backup), res.RestoreGeneration)
		}
	})
	return nil
}

func upgradeRollbackRead(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// upgradeRollbackNeedsRestore says why the store must go back with the
// binary ("" when the previous tw-mgmt can open it as it is).
func upgradeRollbackNeedsRestore(ctx context.Context, s *state, prev string) (string, error) {
	dsn, err := s.Cfg.storeDSN(s.Dir) // up_store.go
	if err != nil {
		return "", err
	}
	if s.Cfg.storeIsSQLite() && !fileExists(filepath.Join(s.Dir, "mgmt", "mgmt.db")) {
		return "", nil // no store yet: nothing to be incompatible with
	}
	st, err := mgmt.CheckSchema(ctx, dsn)
	if err != nil && st.Latest == "" {
		return "", fail("upgrade_failed", "can't read the store's schema to see whether tw "+prev+" can open it: "+err.Error()).
			withFix("tw upgrade rollback --restore   (puts the store back from the backup tw upgrade took)")
	}
	if st.MinBinary == "" {
		return "", nil
	}
	mv, err1 := twversion.Parse(st.MinBinary)
	pv, err2 := twversion.Parse(prev)
	if err1 == nil && err2 == nil && pv.Compare(mv) >= 0 {
		return "", nil
	}
	return fmt.Sprintf("a contract migration ran: the store needs tw-mgmt %s or newer, and the previous release is %s", st.MinBinary, prev), nil
}

// upgradeRollbackBackup is the backup to restore: dir when given, else
// the newest one tw upgrade took before it installed over prev
// (<state>/backups/<time>-<prev>).
func upgradeRollbackBackup(s *state, dir, prev string) (string, error) {
	if dir == "" {
		ds, _ := filepath.Glob(filepath.Join(s.Dir, "backups", "*-"+prev))
		sort.Strings(ds) // <time> sorts
		for i := len(ds) - 1; i >= 0; i-- {
			if fi, err := os.Stat(filepath.Join(ds[i], "mgmt")); err == nil && fi.IsDir() {
				dir = ds[i]
				break
			}
		}
		if dir == "" {
			return "", fail("bad_request", "no backup of tw "+prev+" in "+filepath.Join(s.Dir, "backups")+" (tw upgrade takes one before it installs): the store can't go back").
				withFix("name one: tw upgrade rollback <backup-dir>")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(filepath.Join(abs, "mgmt")); err != nil || !fi.IsDir() {
		return "", fail("bad_request", abs+" is not a tw backup (no mgmt/ directory)").withFix("a tw upgrade backup in <state>/backups/, or tw backup --out <dir>")
	}
	return abs, nil
}

// upgradeRollbackRestore puts backup back as tw restore does (restoreMgmt,
// the hostd data a tw backup holds, the fence for a SQLite store), with
// tailworks stopped and the current state already kept in safety.
func upgradeRollbackRestore(ctx context.Context, s *state, backup, safety string) (store string, gen int64, err error) {
	var m backupManifest // backup.go
	if b, err := os.ReadFile(filepath.Join(backup, backupManifestName)); err == nil {
		if err := json.Unmarshal(b, &m); err != nil || m.V > 1 {
			return "", 0, fmt.Errorf("%s is not a backup manifest this tw reads", backupManifestName)
		}
	} else { // a tw upgrade backup
		m.Store = "postgres"
		if fileExists(filepath.Join(backup, "mgmt", "mgmt.db")) {
			m.Store = "sqlite"
		}
	}
	if m.Store == "sqlite" && !fileExists(filepath.Join(backup, "mgmt", "mgmt.db")) {
		return "", 0, fmt.Errorf("a SQLite install's backup without its store (mgmt/mgmt.db)")
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "mgmt"), 0o700); err != nil {
		return "", 0, err
	}
	if err := restoreMgmt(s, backup); err != nil { // upgrade.go
		return "", 0, err
	}
	if m.Hostd != nil {
		if safety == "" {
			safety = filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-before-rollback")
		}
		if err := restoreHostd(s, backup, safety); err != nil { // backup_hostd.go
			return "", 0, fmt.Errorf("hostd: %w", err)
		}
	}
	if m.Store == "sqlite" {
		if gen, err = mgmt.RestoreFenceDSN(ctx, filepath.Join(s.Dir, "mgmt", "mgmt.db")); err != nil {
			return "", 0, fmt.Errorf("restore fence: %w", err)
		}
	}
	return m.Store, gen, nil
}

// upgradeRollbackConfirmRestore guards the store going back (the typed
// name is "rollback").
func upgradeRollbackConfirmRestore(f *flags, yes bool, backup string) error {
	return confirmDestroy(f, yes, "rolling back with the store restores "+backup+": mgmt's store, custody and tw.json go back to it, and every change made since is lost (the current state is kept in <state>/backups first)",
		"rollback", "tw upgrade rollback --restore", "tw backup   (first, to keep what you have now)")
}

// upgradeRollbackAsk confirms a binary-only rollback (the store stays).
func upgradeRollbackAsk(f *flags, yes bool, cur, prev string) error {
	if yes {
		return nil
	}
	if *f.json || !confirmIsTerminal() {
		return fail("confirmation_required", "tw upgrade rollback would put tw "+prev+" back in place of "+cur+"; it asks first").
			withFix("tw upgrade rollback --yes")
	}
	fmt.Printf("Roll back tailworks %s → %s (the store stays as it is)? [y/N] ", cur, prev)
	ln, _ := bufio.NewReader(confirmIn).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(ln)); a != "y" && a != "yes" {
		return fail("cancelled", "nothing was changed")
	}
	return nil
}
