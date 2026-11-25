package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw custody migrate (issue #295; docs/postgres.md "Move file custody to
// setec"): copy every file custody entry of a tw up install into setec,
// verify the copy, and switch tw.json to setec custody, so the install can
// run several mgmt replicas. Run with tailworks stopped: a write during the
// copy would land in file custody only. File custody is left as it was.

const custodyUsage = "custody migrate --setec-url <url> [--dry-run]"

// custodyMigrateCmd is the exact command that moves the install in dir to
// the setec at setecURL (issue #444): what tw up's custody refusal and
// already_running print. --state-dir is named unless dir is the default.
func custodyMigrateCmd(setecURL, dir string) string {
	cmd := "tw custody migrate --setec-url " + setecURL
	if !isDefaultStateDir(dir, "") && os.Getenv("TW_STATE_DIR") == "" { // up_defaults.go
		cmd += " --state-dir " + dir
	}
	return cmd
}

type custodyMigrateResult struct {
	mgmt.CustodyMigration
	// SafetyBackup is the state before the switch (tw restore it to go back).
	SafetyBackup string `json:"safetyBackup,omitempty"`
	// Switched: tw.json now names setec custody.
	Switched bool `json:"switched"`
}

func cmdCustody(args []string) error {
	f := newFlags("custody")
	// --to: setec is the only target, so the flag says nothing (issue
	// #444). Still accepted (setec only) so earlier scripts keep working.
	to := f.String("to", "", "deprecated: setec is the only target; leave it out")
	setecURL := f.String("setec-url", "", "migrate: the setec server URL to move file custody to (what tw up --setec-url would take)")
	dryRun := f.Bool("dry-run", false, "migrate: map every entry and check setec, but write nothing")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "migrate" {
		return needsSubcommand("custody")
	}
	if *to != "" && *to != "setec" {
		return fail("bad_request", "tw custody migrate moves file custody to setec only; leave --to out", "usage: tw "+custodyUsage)
	}
	u, err := url.Parse(*setecURL)
	if *setecURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fail("bad_request", "tw custody migrate needs --setec-url <http(s)://…>", "usage: tw "+custodyUsage)
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if !s.configured() {
		if s.Link != nil {
			return fail("remote_unsupported", "tw custody runs on the host that runs tw up (this machine is linked to "+s.Link.Endpoint+")")
		}
		return fail("not_up", "tw is not set up in "+s.Dir).withFix("tw up")
	}
	if s.Cfg.Custody == "setec" {
		return fail("state_conflict", "this install already keeps its secrets in setec custody ("+s.Cfg.SetecURL+")")
	}
	if _, act := serviceOf(s); act || s.supervisorAlive() {
		return fail("already_running", "tailworks is running from "+s.Dir+"; moving custody needs it stopped, so no secret is written during the copy").
			withFix("tw down", custodyMigrateCmd(*setecURL, s.Dir), "tw up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res := custodyMigrateResult{}
	dsn, err := s.Cfg.storeDSN(s.Dir)
	if err != nil {
		return err
	}
	st, err := mgmt.OpenStore(ctx, dsn)
	if err != nil {
		return fail("bad_request", "mgmt's store: "+err.Error())
	}
	defer st.Close()
	fc, err := mgmt.OpenFileCustody(filepath.Join(s.Dir, "mgmt", "custody"))
	if err != nil {
		return err
	}
	dst := &mgmt.SetecCustody{BaseURL: *setecURL}
	rep, err := mgmt.MigrateFileCustody(ctx, st, fc, dst, time.Now().UnixMilli(), *dryRun)
	res.CustodyMigration = rep
	switch {
	case errors.Is(err, mgmt.ErrCustodyUnmapped):
		lines := []string{"no store row and no custody_names entry names these files in " + fc.SecretsDir() + ", so nothing in tailworks reads them:"}
		for _, n := range rep.Unmapped {
			lines = append(lines, "  "+n)
		}
		lines = append(lines, "they are left by a write that was interrupted, or by data deleted before custody names were indexed")
		return fail("custody_unmapped", fmt.Sprintf("%d of %d custody entries map to no name; nothing was copied", len(rep.Unmapped), rep.Files), lines...).
			withFix("move them out of "+fc.SecretsDir()+" (e.g. into "+filepath.Join(s.Dir, "mgmt", "custody-unmapped")+"), then run tw custody migrate again",
				"or keep file custody: leave the install as it is")
	case errors.Is(err, mgmt.ErrCustodyConflict):
		return fail("state_conflict", fmt.Sprintf("setec already holds a different value under %d of this install's names (%s); nothing was copied", len(rep.Conflicts), strings.Join(rep.Conflicts, ", ")),
			"another install may use the same setec and names").
			withFix("check what else uses " + *setecURL + "; delete those names there only if they are stale")
	case err != nil:
		return fail("custody_migrate_failed", "custody migrate: "+err.Error()).
			withFix("fix the cause and run tw custody migrate again (entries already in setec with the same value are kept); tw.json still names file custody")
	}
	if !*dryRun {
		// What the switch replaces is kept, so it can be undone.
		safety := filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-before-custody-migrate")
		if _, err := backupMgmtTo(ctx, s, safety); err != nil { // upgrade.go
			return fail("backup_failed", "every entry is in setec, but the state could not be backed up before switching tw.json: "+err.Error()).
				withFix("free space in " + filepath.Join(s.Dir, "backups") + ", then run tw custody migrate again")
		}
		res.SafetyBackup = safety
		s.Cfg.Custody, s.Cfg.SetecURL = "setec", *setecURL
		if err := s.save(); err != nil {
			return fail("custody_migrate_failed", "every entry is in setec, but tw.json was not switched: "+err.Error()).
				withFix("run tw custody migrate again")
		}
		res.Switched = true
	}
	emit(f, res, func() {
		verb := "copied"
		if rep.DryRun {
			verb = "would copy"
		}
		fmt.Printf("✓ custody  %s %d entries %s → %s (%d already there)\n", verb, len(rep.Names)-rep.Present, rep.From, rep.To, rep.Present)
		if len(rep.Backfilled) > 0 {
			fmt.Printf("  indexed %d names written before custody_names existed\n", len(rep.Backfilled))
		}
		if len(rep.Partial) > 0 {
			fmt.Printf("  skipped %d unfinished writes (*.tmp) in %s\n", len(rep.Partial), fc.SecretsDir())
		}
		if rep.DryRun {
			fmt.Println("  dry run: nothing written; run it again without --dry-run")
			return
		}
		fmt.Printf("✓ verify   %d entries read back the same from setec\n", rep.Verified)
		fmt.Printf("✓ tw.json  custody: setec (%s)\n", *setecURL)
		fmt.Printf("  the state before: %s (tw restore it to go back)\n", res.SafetyBackup)
		fmt.Printf("  file custody is unchanged in %s; remove it once tailworks has served from setec\n", filepath.Join(s.Dir, "mgmt", "custody"))
		fmt.Println("  next: tw up")
	})
	return nil
}
