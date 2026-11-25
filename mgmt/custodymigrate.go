package mgmt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Moving file custody to setec (issue #295, `tw custody migrate`).
//
// File custody keeps each entry under a hash of its name, so the entries
// can't be listed by name. The migration maps every file back to a name
// mgmt can derive, and refuses if any file is left over:
//
//   - Every name in custody_names. mgmt indexes each tenant name before it
//     writes it (indexedCustody, tenantpurge.go), so everything written
//     since the index existed is there.
//   - Every name the store's rows imply, for entries written before the
//     index (tenantCustodyCandidates, the same list tenant purge deletes):
//     each workload's secrets and volume key, each store's and queue's data
//     key, each object's auth key (keyed and legacy), each warm pool key,
//     the tenant's notification and telemetry settings and OBO root keys;
//     plus the shared ID secret its secret_choice row names.
//   - The secrets of every workload the audit chain records a secret change
//     for: secrets can be set on a workload that was never deployed, and
//     then no other row names it.
//
// A name only counts when its file decrypts: file custody binds the name
// as additional data, so a match proves the file is that name's entry.
//
// What can't be mapped is an entry no row and no index names. mgmt never
// computes its name, so nothing reads it: a write interrupted between
// custody and the store, or data deleted before the index existed (or,
// for a never-deployed workload's secrets, audit records pruned by
// retention before the index existed). The
// migration refuses rather than drop it silently; the operator moves such
// files out of custody/secrets and runs it again.
//
// BackfillCustodyIndex adds the derived names to custody_names first, so
// the index covers every entry, and the migration itself copies each
// entry, then reads every one back from setec and compares.

// ErrCustodyUnmapped: file custody holds entries no known name maps to.
var ErrCustodyUnmapped = errors.New("custody: entries that map to no name")

// ErrCustodyConflict: the target already holds a different value under a
// name being copied.
var ErrCustodyConflict = errors.New("custody: the target holds a different value")

// CustodyMigration is what MigrateFileCustody did (or, with DryRun, would
// do). It never holds a value.
type CustodyMigration struct {
	From   string `json:"from"` // CustodyFingerprint
	To     string `json:"to"`
	DryRun bool   `json:"dryRun"`
	// Files is how many entries file custody holds.
	Files int `json:"files"`
	// Names are the entries' names, sorted.
	Names []string `json:"names"`
	// Backfilled are the names added to custody_names (with DryRun: the
	// names that would be).
	Backfilled []string `json:"backfilled"`
	// Unmapped are the files no name maps to (under custody/secrets).
	Unmapped []string `json:"unmapped,omitempty"`
	// Partial are leftover *.tmp files of interrupted writes. The write
	// never completed, so they are not entries and are not copied.
	Partial []string `json:"partial,omitempty"`
	// Conflicts are names the target holds with a different value.
	Conflicts []string `json:"conflicts,omitempty"`
	// Copied were written to the target; Present already held the same
	// value there (a migration run again after an interruption).
	Copied  int `json:"copied"`
	Present int `json:"present"`
	// Verified were read back from the target and match.
	Verified int `json:"verified"`
}

// fileName is the file under secrets/ that holds name's entry.
func (c *FileCustody) fileName(name string) string { return filepath.Base(c.path(name)) }

// SecretsDir is where the entries are (one file per entry).
func (c *FileCustody) SecretsDir() string { return filepath.Join(c.dir, "secrets") }

// files lists the entry files and the leftover *.tmp files.
func (c *FileCustody) files() (entries, partial []string, err error) {
	des, err := os.ReadDir(c.SecretsDir())
	if err != nil {
		return nil, nil, err
	}
	for _, d := range des {
		switch {
		case strings.HasSuffix(d.Name(), ".tmp"):
			partial = append(partial, d.Name())
		default:
			entries = append(entries, d.Name())
		}
	}
	return entries, partial, nil
}

// custodyCandidates is every custody name the store can name: the index,
// and what each tenant's rows (and the ID secret's choice row) imply.
func custodyCandidates(ctx context.Context, st custodyIndexStore) (all []string, indexed map[string]bool, err error) {
	idx, err := st.IndexedCustodyNames(ctx)
	if err != nil {
		return nil, nil, err
	}
	indexed = map[string]bool{}
	set := map[string]bool{}
	for _, n := range idx {
		indexed[n], set[n] = true, true
	}
	if kid, err := st.SecretChoice(ctx, idSecretChoice); err == nil {
		set[idSecretCustodyName(kid)] = true
	} else if !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	tenants, err := st.CustodyTenantIDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	secretWorkloads, err := st.SecretWorkloads(ctx)
	if err != nil {
		return nil, nil, err
	}
	for id, ws := range secretWorkloads {
		for _, w := range ws {
			set[secretsCustodyName(id, w)] = true
		}
	}
	for _, id := range tenants {
		refs, err := st.TenantCustodyRefs(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		for _, n := range tenantCustodyCandidates(ctx, st, id, refs) {
			set[n] = true
		}
	}
	for n := range set {
		all = append(all, n)
	}
	sort.Strings(all)
	return all, indexed, nil
}

func asCustodyIndexStore(st Store) (custodyIndexStore, error) {
	s, ok := st.(custodyIndexStore)
	if !ok {
		return nil, fmt.Errorf("custody: store %T can't list custody names", st)
	}
	return s, nil
}

// BackfillCustodyIndex adds to custody_names every tenant name the store's
// rows imply whose entry is in fc, so the index covers entries written
// before it existed. It returns the names added (with dryRun, the names it
// would add). The ID secret is install-wide and stays out of the index
// (it is named by its secret_choice row).
func BackfillCustodyIndex(ctx context.Context, st Store, fc *FileCustody, now int64, dryRun bool) ([]string, error) {
	s, err := asCustodyIndexStore(st)
	if err != nil {
		return nil, err
	}
	all, indexed, err := custodyCandidates(ctx, s)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, n := range all {
		t := custodyTenant(n)
		if indexed[n] || t == "" || t == "mgmt" {
			continue
		}
		if _, err := fc.Get(ctx, n); errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return added, fmt.Errorf("custody entry %s: %w", n, err)
		}
		if !dryRun {
			if err := s.IndexCustodyName(ctx, n, t, now); err != nil {
				return added, err
			}
		}
		added = append(added, n)
	}
	return added, nil
}

// MigrateFileCustody copies every entry of fc into dst (setec). It first
// backfills custody_names, then maps every file back to its name and
// refuses (ErrCustodyUnmapped) if any is left over, and refuses
// (ErrCustodyConflict) if dst holds a different value under one of the
// names, before writing anything. After the copy it reads every entry back
// from dst and compares. fc is left as it was. Run it with mgmt stopped: a
// write during the copy would land in fc only.
func MigrateFileCustody(ctx context.Context, st Store, fc *FileCustody, dst Custody, now int64, dryRun bool) (CustodyMigration, error) {
	rep := CustodyMigration{From: CustodyFingerprint(fc), To: CustodyFingerprint(dst), DryRun: dryRun, Names: []string{}, Backfilled: []string{}}
	s, err := asCustodyIndexStore(st)
	if err != nil {
		return rep, err
	}
	if rep.Backfilled, err = BackfillCustodyIndex(ctx, st, fc, now, dryRun); err != nil {
		return rep, fmt.Errorf("backfill custody_names: %w", err)
	}
	if rep.Backfilled == nil {
		rep.Backfilled = []string{}
	}
	all, _, err := custodyCandidates(ctx, s)
	if err != nil {
		return rep, err
	}
	byFile := make(map[string]string, len(all))
	for _, n := range all {
		byFile[fc.fileName(n)] = n
	}
	files, partial, err := fc.files()
	if err != nil {
		return rep, err
	}
	rep.Files, rep.Partial = len(files), partial
	values := map[string][]byte{}
	for _, f := range files {
		n, ok := byFile[f]
		if !ok {
			rep.Unmapped = append(rep.Unmapped, f)
			continue
		}
		v, err := fc.Get(ctx, n)
		if err != nil {
			return rep, fmt.Errorf("custody entry %s: %w", n, err)
		}
		values[n] = v
		rep.Names = append(rep.Names, n)
	}
	sort.Strings(rep.Names)
	if len(rep.Unmapped) > 0 {
		return rep, fmt.Errorf("%w: %d of %d files in %s", ErrCustodyUnmapped, len(rep.Unmapped), len(files), fc.SecretsDir())
	}
	var todo []string
	for _, n := range rep.Names {
		got, err := dst.Get(ctx, n)
		switch {
		case errors.Is(err, ErrNotFound):
			todo = append(todo, n)
		case err != nil:
			return rep, fmt.Errorf("%s: %w", rep.To, err)
		case bytes.Equal(got, values[n]):
			rep.Present++
		default:
			rep.Conflicts = append(rep.Conflicts, n)
		}
	}
	if len(rep.Conflicts) > 0 {
		return rep, fmt.Errorf("%w: %d names", ErrCustodyConflict, len(rep.Conflicts))
	}
	if dryRun {
		return rep, nil
	}
	for _, n := range todo {
		if err := dst.Put(ctx, n, values[n]); err != nil {
			return rep, fmt.Errorf("copy %s: %w", n, err)
		}
		rep.Copied++
	}
	for _, n := range rep.Names {
		got, err := dst.Get(ctx, n)
		if err != nil {
			return rep, fmt.Errorf("verify %s: %w", n, err)
		}
		if !bytes.Equal(got, values[n]) {
			return rep, fmt.Errorf("verify %s: the value read back differs", n)
		}
		rep.Verified++
	}
	return rep, nil
}
