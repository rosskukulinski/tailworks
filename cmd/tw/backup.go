package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw backup and tw restore (issue #120; docs/INSTALL.md "Back up and
// restore"). A backup is what tw upgrade takes before it installs
// (backupMgmtTo): the store as a consistent VACUUM INTO copy, taken while
// tailworks runs, the rest of <state>/mgmt that must match it (custody, the
// ID secret, mgmt's node state), and tw.json; plus backup.json, which says
// what it holds. tw restore puts one back with tailworks stopped, then runs
// the restore fence: every lease epoch jumps, so no fencing token handed
// out after the backup is ever reused.

// backupManifest is <backup>/backup.json.
type backupManifest struct {
	V         int    `json:"v"`
	Version   string `json:"version"` // the tw that wrote it
	CreatedAt string `json:"createdAt"`
	StateDir  string `json:"stateDir"`
	// Store is "sqlite" (mgmt/mgmt.db is in the backup) or "postgres" (it
	// is not: back the database up with pg_dump).
	Store string `json:"store"`
	// Custody is "file" (mgmt/custody is in the backup) or "setec" (the
	// secrets stay in setec).
	Custody string             `json:"custody"`
	Schema  *mgmt.SchemaStatus `json:"schema,omitempty"`
	// Hostd is what of <state>/hostd is in the backup (Durable Object
	// storage and fs R2 bytes); nil when tw backup --hostd=false (or an
	// older tw) wrote it.
	Hostd *hostdBackup `json:"hostd,omitempty"`
	// NotIncluded lists what lives elsewhere and needs its own backup.
	NotIncluded []string `json:"notIncluded"`
}

const backupManifestName = "backup.json"

type backupResult struct {
	Backup   string         `json:"backup"`
	Manifest backupManifest `json:"manifest"`
	Bytes    int64          `json:"bytes"`
}

func cmdBackup(args []string) error {
	f := newFlags("backup")
	out := f.String("out", "", "write the backup to this new directory (default <state>/backups/<time>-backup-<version>)")
	withHostd := f.Bool("hostd", true, "also copy each workload's Durable Object storage and the fs backend's R2 bytes (--hostd=false: the control plane only)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fail("bad_request", "tw backup takes no arguments", "usage: tw "+usageOf("backup"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if !s.configured() {
		if s.Link != nil {
			return fail("remote_unsupported", "tw backup runs on the host that runs tw up (this machine is linked to "+s.Link.Endpoint+")")
		}
		return fail("not_up", "tw is not set up in "+s.Dir).withFix("tw up")
	}
	dst := *out
	if dst == "" {
		dst = filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-backup-"+version)
	}
	if dst, err = filepath.Abs(dst); err != nil {
		return err
	}
	if rel, err := filepath.Rel(filepath.Join(s.Dir, "mgmt"), dst); err == nil && !strings.HasPrefix(rel, "..") {
		return fail("bad_request", "--out "+dst+" is inside the state it backs up").withFix("tw backup --out <a directory outside " + filepath.Join(s.Dir, "mgmt") + ">")
	}
	if rel, err := filepath.Rel(filepath.Join(s.Dir, "hostd"), dst); err == nil && !strings.HasPrefix(rel, "..") {
		return fail("bad_request", "--out "+dst+" is inside the state it backs up").withFix("tw backup --out <a directory outside " + filepath.Join(s.Dir, "hostd") + ">")
	}
	if ents, err := os.ReadDir(dst); err == nil && len(ents) > 0 {
		return fail("bad_request", dst+" already exists and is not empty").withFix("tw backup --out <a new directory>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	mgmtSnapshot := func() error {
		_, err := backupMgmtTo(ctx, s, dst) // upgrade.go
		return err
	}
	var hb *hostdBackup
	if *withHostd {
		hb, err = backupHostd(ctx, s, dst, mgmtSnapshot) // backup_hostd.go
	} else {
		err = mgmtSnapshot()
	}
	if err != nil {
		os.RemoveAll(dst)
		return fail("backup_failed", "backup: "+err.Error())
	}
	m := backupManifest{V: 1, Version: version, CreatedAt: time.Now().UTC().Format(time.RFC3339), StateDir: s.Dir,
		Store: "sqlite", Custody: custodyName(s.Cfg.Custody), Hostd: hb,
		NotIncluded: append(hostdNotIncluded(filepath.Join(s.Dir, "hostd"), *withHostd),
			filepath.Join(s.Dir, "mgmt", "registry")+" (container image blobs: copy it separately if you deploy container images)",
		)}
	if hb != nil {
		m.NotIncluded = append(m.NotIncluded, hb.Skipped...)
	}
	if !s.Cfg.storeIsSQLite() {
		m.Store = "postgres"
		m.NotIncluded = append(m.NotIncluded, "the Postgres store (pg_dump it at the same time)")
	} else if st, err := mgmt.CheckSchema(ctx, filepath.Join(dst, "mgmt", "mgmt.db")); err == nil {
		m.Schema = &st
	}
	if m.Custody == "setec" {
		m.NotIncluded = append(m.NotIncluded, "setec custody (the secrets stay in setec)")
	}
	if s.Cfg.OAuthClientFile != "" {
		m.NotIncluded = append(m.NotIncluded, s.Cfg.OAuthClientFile+" (the OAuth client file)")
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dst, backupManifestName), append(b, '\n'), 0o600); err != nil {
		return fail("backup_failed", "backup: "+err.Error())
	}
	os.Chmod(dst, 0o700)
	res := backupResult{Backup: dst, Manifest: m, Bytes: dirBytes(dst)}
	emit(f, res, func() {
		fmt.Printf("✓ backup   %s (%s; store %s, custody %s)\n", dst, humanBytes(res.Bytes), m.Store, m.Custody)
		if hb != nil {
			fmt.Printf("✓ hostd    DO storage of %d workload(s) (%d databases), %d R2 blob(s) (%s)\n", len(hb.Workloads), hb.Databases, hb.R2Blobs, humanBytes(hb.R2Bytes))
		}
		fmt.Println("  it holds the custody key and mgmt's node key: keep it like a secret")
		for _, n := range m.NotIncluded {
			fmt.Println("  not included:", n)
		}
		fmt.Printf("  restore: tw down && tw restore %s && tw up\n", dst)
	})
	return nil
}

type restoreResult struct {
	Restored string `json:"restored"`
	// SafetyBackup is the state the restore replaced (tw restore it to go
	// back).
	SafetyBackup string `json:"safetyBackup,omitempty"`
	Store        string `json:"store"`
	// RestoreGeneration counts the store's restores (SQLite stores; a
	// Postgres store is fenced with tw-mgmt restore-fence after pg_restore).
	RestoreGeneration int64 `json:"restoreGeneration,omitempty"`
	// Hostd is whether the backup's Durable Object storage and R2 bytes
	// were put back.
	Hostd bool `json:"hostd,omitempty"`
}

func cmdRestore(args []string) error {
	f := newFlags("restore")
	yes := yesFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw restore takes the backup directory", "usage: tw "+usageOf("restore"))
	}
	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	if err := confirmDestroy(f, *yes, "restoring "+pos[0]+" replaces this install's control plane (mgmt's store, custody and tw.json) and, when the backup holds them, its Durable Object storage and R2 bytes with the backup's; every change made after the backup is lost", pos[0], "tw restore "+pos[0],
		"tw backup   (first, to keep what you have now)"); err != nil {
		return err
	}
	if fi, err := os.Stat(filepath.Join(dir, "mgmt")); err != nil || !fi.IsDir() {
		return fail("bad_request", dir+" is not a tw backup (no mgmt/ directory)").withFix("tw backup --out <dir>, or a tw upgrade backup in <state>/backups/")
	}
	var m backupManifest
	if b, err := os.ReadFile(filepath.Join(dir, backupManifestName)); err == nil {
		if err := json.Unmarshal(b, &m); err != nil || m.V > 1 {
			return fail("bad_request", dir+"/"+backupManifestName+" is not a backup manifest this tw reads")
		}
	} else { // a tw upgrade backup
		m.Store = "postgres"
		if fileExists(filepath.Join(dir, "mgmt", "mgmt.db")) {
			m.Store = "sqlite"
		}
	}
	if m.Store == "sqlite" && !fileExists(filepath.Join(dir, "mgmt", "mgmt.db")) {
		return fail("bad_request", dir+" is a SQLite install's backup without its store (mgmt/mgmt.db)")
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if s.Link != nil && !s.configured() {
		return fail("remote_unsupported", "tw restore runs on the host that runs tw up (this machine is linked to "+s.Link.Endpoint+")")
	}
	if _, act := serviceOf(s); act || s.supervisorAlive() {
		return fail("already_running", "tailworks is running from "+s.Dir+"; a restore needs it stopped").withFix("tw down", "tw restore "+pos[0]+" --yes", "tw up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res := restoreResult{Restored: dir, Store: m.Store}
	if fileExists(filepath.Join(s.Dir, "mgmt")) {
		// What is replaced is kept, so the restore itself can be undone.
		safety := filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-before-restore")
		if _, err := backupMgmtTo(ctx, s, safety); err != nil {
			return fail("restore_failed", "could not back up the current state first: "+err.Error()).
				withFix("move " + filepath.Join(s.Dir, "mgmt") + " aside yourself, then tw restore again")
		}
		res.SafetyBackup = safety
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "mgmt"), 0o700); err != nil {
		return err
	}
	if err := restoreMgmt(s, dir); err != nil { // upgrade.go
		return fail("restore_failed", "restore: "+err.Error()).withFix("tw restore " + res.SafetyBackup + " --yes   (puts the state from before back)")
	}
	if m.Hostd != nil {
		safety := res.SafetyBackup
		if safety == "" {
			safety = filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-before-restore")
		}
		if err := restoreHostd(s, dir, safety); err != nil { // backup_hostd.go
			return fail("restore_failed", "restore hostd: "+err.Error()).withFix("tw restore " + safety + " --yes   (puts the state from before back)")
		}
		res.Hostd = true
	}
	if m.Store == "sqlite" {
		gen, err := mgmt.RestoreFenceDSN(ctx, filepath.Join(s.Dir, "mgmt", "mgmt.db"))
		if err != nil {
			return fail("restore_failed", "restore fence: "+err.Error())
		}
		res.RestoreGeneration = gen
	}
	emit(f, res, func() {
		fmt.Printf("✓ restore  %s → %s\n", dir, s.Dir)
		if res.Hostd {
			fmt.Println("✓ hostd    Durable Object storage and R2 bytes as of the backup")
		}
		if res.SafetyBackup != "" {
			fmt.Printf("  the state it replaced: %s\n", res.SafetyBackup)
		}
		if m.Store == "sqlite" {
			fmt.Printf("✓ fence    every lease epoch moved up by %d (restore generation %d)\n", mgmt.RestoreEpochJump, res.RestoreGeneration)
			fmt.Println("  next: tw up")
		} else {
			fmt.Println("  the store is Postgres: restore it from the pg_dump taken with this backup, then fence it before tw up:")
			fmt.Println("    pg_restore --clean --if-exists -d <dsn> <dump>")
			fmt.Println("    tw-mgmt restore-fence -db-file <file with the DSN>")
			fmt.Println("    tw up")
		}
	})
	return nil
}

// dirBytes is the size of the regular files under dir.
func dirBytes(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}
