package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// tw backup's hostd part (issue #295): each workload's Durable Object
// storage and, with the fs backend, R2 object bytes, copied while
// tailworks runs.
//
//   - Durable Object storage (<hostd>/tenants/<t>/workloads/<w>/do) is one
//     SQLite database per object (and per facet), plus each namespace's
//     metadata.sqlite, all in WAL mode and open in workerd. workerd opens
//     them through SQLite's native VFS, so its POSIX locks and the WAL
//     index (-shm) are shared with any other process: a read-only
//     connection here sees the last committed transaction, never a torn
//     one, and doesn't block workerd's writers. Each database is copied
//     with VACUUM INTO; -wal and -shm files are not copied (the copy is
//     self-contained). Each database is consistent on its own; two objects
//     are not copied at the same instant (no Durable Object transaction
//     spans two objects, so none is torn).
//   - R2 bytes (<hostd>/r2) are immutable blobs: a put writes a temporary
//     ".put-*" file and renames it into place, and a blob is never
//     rewritten, only deleted once no index entry points to it. So a blob
//     file is whole or absent, and copying them during writes is safe. The
//     index lives in mgmt's store, so the blobs are copied twice around
//     mgmt's snapshot: before it (every blob that may be deleted later is
//     kept) and after it (blobs written while the store was copied). A
//     blob is hard-linked into the backup when it is on the same file
//     system (immutable, so a link is a copy), and copied otherwise.
//   - A microVM workload's DO volume (do.img) is a disk image the guest
//     has mounted; it can't be copied consistently from outside, so it is
//     listed as skipped.

// hostdBackup is backup.json's "hostd": what tw backup copied from
// <state>/hostd.
type hostdBackup struct {
	// Workloads are the "<tenant>/<workload>" whose Durable Object storage
	// is in the backup.
	Workloads []string `json:"workloads"`
	// Databases is how many SQLite databases were copied (VACUUM INTO).
	Databases int `json:"databases"`
	// Files is how many other files of DO storage were copied as they are.
	Files int `json:"files"`
	// R2Blobs and R2Bytes count the fs backend's blobs in the backup.
	R2Blobs int   `json:"r2Blobs"`
	R2Bytes int64 `json:"r2Bytes"`
	// Skipped lists what is not in it and why (microVM DO volumes).
	Skipped []string `json:"skipped,omitempty"`
}

// hostdDODirs lists <hostd>/tenants/*/workloads/*/do, as paths relative to
// hostd.
func hostdDODirs(hostd string) []string {
	ms, _ := filepath.Glob(filepath.Join(hostd, "tenants", "*", "workloads", "*", "do"))
	var out []string
	for _, m := range ms {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			rel, _ := filepath.Rel(hostd, m)
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// backupHostdDO copies every workload's DO storage from hostd (the live
// <state>/hostd) into dst/hostd.
func backupHostdDO(ctx context.Context, hostd, dst string, hb *hostdBackup) error {
	for _, rel := range hostdDODirs(hostd) {
		parts := strings.Split(filepath.ToSlash(rel), "/") // tenants/<t>/workloads/<w>/do
		hb.Workloads = append(hb.Workloads, parts[1]+"/"+parts[3])
		src := filepath.Join(hostd, rel)
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) { // deleted while we walk
					return nil
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			r, _ := filepath.Rel(src, p)
			t := filepath.Join(dst, "hostd", rel, r)
			if d.IsDir() {
				return os.MkdirAll(t, 0o700)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal") {
				return nil
			}
			isDB, err := isSQLiteFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if isDB {
				if err := snapshotSQLite(ctx, p, t); err != nil {
					if errors.Is(err, fs.ErrNotExist) || !fileExists(p) { // the object was deleted
						os.Remove(t)
						return nil
					}
					return fmt.Errorf("%s: %w", p, err)
				}
				hb.Databases++
				return nil
			}
			if err := copyFile(p, t); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			hb.Files++
			return nil
		})
		if err != nil {
			return err
		}
	}
	vols, _ := filepath.Glob(filepath.Join(hostd, "tenants", "*", "workloads", "*", "do.img"))
	sort.Strings(vols)
	for _, v := range vols {
		hb.Skipped = append(hb.Skipped, v+" (a microVM workload's DO volume: tw down, then copy it)")
	}
	return nil
}

var sqliteMagic = []byte("SQLite format 3\x00")

// isSQLiteFile reports whether p starts with SQLite's header. An empty
// file (an object whose database workerd has just created) is not one,
// and is copied as it is.
func isSQLiteFile(p string) (bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, b); err != nil {
		return false, nil
	}
	return bytes.Equal(b, sqliteMagic), nil
}

// snapshotSQLite copies the live database src into dst with VACUUM INTO,
// through a read-only connection: one read transaction, so the copy is the
// database as of one commit, while the owner keeps writing.
func snapshotSQLite(ctx context.Context, src, dst string) error {
	os.Remove(dst)
	u := url.URL{Scheme: "file", Path: src, RawQuery: "mode=ro&_pragma=busy_timeout(10000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0o600)
}

// backupHostdR2 copies the fs backend's blobs (<hostd>/r2) into dst/hostd/r2,
// skipping those already there (the second pass) and unfinished puts.
func backupHostdR2(ctx context.Context, hostd, dst string, hb *hostdBackup) error {
	src := filepath.Join(hostd, "r2")
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, "hostd", "r2", r)
		if d.IsDir() {
			return os.MkdirAll(t, 0o700)
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".put-") {
			return nil
		}
		if _, err := os.Lstat(t); err == nil {
			return nil
		}
		if err := os.Link(p, t); err != nil {
			if errors.Is(err, fs.ErrNotExist) { // deleted since the walk saw it
				return nil
			}
			if err := copyFile(p, t); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
		}
		hb.R2Blobs++
		if fi, err := os.Stat(t); err == nil {
			hb.R2Bytes += fi.Size()
		}
		return nil
	})
}

// copyFile copies src to dst through a temporary file, so dst is whole or
// absent.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// restoreHostd puts a backup's hostd/ back (tailworks stopped): every
// workload's DO storage and the R2 blobs are replaced by the backup's, so
// the host's data is as of the backup, like mgmt's. What is replaced moves
// into safety/hostd (the safety backup), so the restore can be undone. The
// rest of <state>/hostd (node state, logs, objects.json) stays.
func restoreHostd(s *state, backup, safety string) error {
	hostd := filepath.Join(s.Dir, "hostd")
	src := filepath.Join(backup, "hostd")
	cur := hostdDODirs(hostd)
	if fileExists(filepath.Join(hostd, "r2")) {
		cur = append(cur, "r2")
	}
	moved := &hostdBackup{Workloads: []string{}}
	for _, rel := range cur {
		if err := moveAside(filepath.Join(hostd, rel), filepath.Join(safety, "hostd", rel)); err != nil {
			return err
		}
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) == 5 {
			moved.Workloads = append(moved.Workloads, parts[1]+"/"+parts[3])
		}
	}
	// The safety backup says it holds hostd/ too, so tw restore of it puts
	// the replaced DO storage and blobs back.
	if mp := filepath.Join(safety, backupManifestName); !fileExists(mp) && fileExists(safety) {
		m := backupManifest{V: 1, Version: version, CreatedAt: time.Now().UTC().Format(time.RFC3339), StateDir: s.Dir,
			Store: "sqlite", Custody: custodyName(s.Cfg.Custody), Hostd: moved, NotIncluded: []string{}}
		if !s.Cfg.storeIsSQLite() {
			m.Store = "postgres"
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(mp, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	put := hostdDODirs(src)
	if fileExists(filepath.Join(src, "r2")) {
		put = append(put, "r2")
	}
	for _, rel := range put {
		t := filepath.Join(hostd, rel)
		if err := os.MkdirAll(filepath.Dir(t), 0o700); err != nil {
			return err
		}
		if err := copyTree(filepath.Join(src, rel), t); err != nil {
			return err
		}
	}
	return nil
}

// moveAside moves dir to dst (a rename, or a copy and delete across file
// systems).
func moveAside(dir, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(dir, dst); err == nil {
		return nil
	}
	if err := copyTree(dir, dst); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// hostdNotIncluded is what of <state>/hostd a backup doesn't hold.
func hostdNotIncluded(hostd string, withHostd bool) []string {
	if !withHostd {
		return []string{hostd + " (Durable Object storage and R2 object bytes: tw backup without --hostd=false copies them)"}
	}
	out := []string{hostd + " node state, logs and objects.json (tw restore leaves this host's copy in place)"}
	if ms, _ := filepath.Glob(filepath.Join(hostd, "tenants", "*", "workloads", "*", "node")); len(ms) > 0 {
		out = append(out, filepath.Join(hostd, "tenants", "*", "workloads", "*", "node")+" (Node.js workloads' data directories: tw down, then copy them)")
	}
	return out
}

// backupHostd is tw backup's hostd part around backupMgmtTo: R2 blobs
// before and after mgmt's snapshot, DO storage after it.
func backupHostd(ctx context.Context, s *state, dst string, mgmtSnapshot func() error) (*hostdBackup, error) {
	hostd := filepath.Join(s.Dir, "hostd")
	hb := &hostdBackup{Workloads: []string{}}
	if err := backupHostdR2(ctx, hostd, dst, hb); err != nil {
		return nil, fmt.Errorf("hostd R2 blobs: %w", err)
	}
	if err := mgmtSnapshot(); err != nil {
		return nil, err
	}
	if err := backupHostdR2(ctx, hostd, dst, hb); err != nil {
		return nil, fmt.Errorf("hostd R2 blobs: %w", err)
	}
	if err := backupHostdDO(ctx, hostd, dst, hb); err != nil {
		return nil, fmt.Errorf("hostd DO storage: %w", err)
	}
	return hb, nil
}
