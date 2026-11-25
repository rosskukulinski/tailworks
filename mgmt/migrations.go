package mgmt

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/version"
)

// Numbered store migrations (ADR-041 §3; the rules are
// in migrations/README.md). The DDL in migrate() is the baseline: it is
// idempotent and still runs on every open, so stores created before this
// file adopt the scheme unchanged. Everything newer is one file in
// migrations/.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// BaselineMigration is the ID recorded for the pre-migrations schema.
const BaselineMigration = "000000000000_baseline"

// schemaMeta keys.
const (
	metaMinBinary         = "min_binary_version"
	metaRestoreGeneration = "restore_generation"
)

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  id TEXT PRIMARY KEY, applied_at BIGINT NOT NULL, applied_by TEXT NOT NULL DEFAULT '',
  contract BIGINT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS schema_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
`

// Migration is one file in migrations/.
type Migration struct {
	ID       string // <yyyymmddhhmm>_<slug>
	Contract bool   // carries "-- tw:contract"
	common   string
	pg       string
	sqlite   string
}

func (m Migration) sql(d dialect) string {
	s := m.common
	switch {
	case d == dialectPostgres && m.pg != "":
		s = m.pg
	case d == dialectSQLite && m.sqlite != "":
		s = m.sqlite
	}
	blob := "BLOB"
	if d == dialectPostgres {
		blob = "BYTEA"
	}
	return strings.ReplaceAll(s, "{{blob}}", blob)
}

// Migrations lists the embedded migrations in apply (ID) order.
func Migrations() []Migration {
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		panic(err) // a malformed embedded file is a build-time bug (TestMigrationFiles)
	}
	return ms
}

func loadMigrations(fsys fs.FS) ([]Migration, error) {
	files, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	byID := map[string]*Migration{}
	for _, f := range files {
		name := path.Base(f)
		base := strings.TrimSuffix(name, ".sql")
		variant := ""
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base, variant = base[:i], base[i+1:]
		}
		if !validMigrationID(base) {
			return nil, fmt.Errorf("migration %s: want <yyyymmddhhmm>_<slug>.sql", name)
		}
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		m := byID[base]
		if m == nil {
			m = &Migration{ID: base}
			byID[base] = m
		}
		body := string(b)
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "-- tw:contract" {
				m.Contract = true
			}
		}
		switch variant {
		case "":
			m.common = body
		case "pg":
			m.pg = body
		case "sqlite":
			m.sqlite = body
		default:
			return nil, fmt.Errorf("migration %s: unknown variant %q (want .pg.sql or .sqlite.sql)", name, variant)
		}
	}
	out := make([]Migration, 0, len(byID))
	for _, m := range byID {
		if m.common == "" && (m.pg == "" || m.sqlite == "") {
			return nil, fmt.Errorf("migration %s: needs %s.sql, or both %s.pg.sql and %s.sqlite.sql", m.ID, m.ID, m.ID, m.ID)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func validMigrationID(id string) bool {
	if len(id) < 14 || id[12] != '_' {
		return false
	}
	for _, c := range id[:12] {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, c := range id[13:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// SchemaStatus is where a store's schema stands relative to this binary.
type SchemaStatus struct {
	// Latest is the newest migration this binary knows; Applied the newest
	// applied to the store.
	Latest  string `json:"latest"`
	Applied string `json:"applied"`
	// Pending are known migrations not yet applied (empty after open).
	Pending []string `json:"pending,omitempty"`
	// PendingContract are the pending contract migrations.
	PendingContract []string `json:"pendingContract,omitempty"`
	// Ahead are applied migrations this binary doesn't know: the store
	// was migrated by a newer tw-mgmt (allowed while MinBinary permits).
	Ahead []string `json:"ahead,omitempty"`
	// MinBinary is the oldest tw-mgmt version that may open the store
	// ("" = any).
	MinBinary string `json:"minBinary,omitempty"`
	// RestoreGeneration counts restores from backup (lease epochs jump
	// past every epoch handed out before each one).
	RestoreGeneration int64 `json:"restoreGeneration,omitempty"`
}

// errSchemaTooNew: the store was contracted by a newer tw-mgmt.
type errSchemaTooNew struct{ min, self string }

func (e errSchemaTooNew) Error() string {
	return fmt.Sprintf("version_skew: this store was migrated by tw-mgmt %s or newer (a contract migration), and this tw-mgmt is %s; run tw-mgmt %s or newer, or restore the pre-upgrade backup", e.min, e.self, e.min)
}

// readSchema reads the applied migrations and meta (tables must exist).
func readSchema(ctx context.Context, db *sql.DB) (map[string]bool, map[string]string, error) {
	applied := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT id FROM schema_migrations")
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		applied[id] = true
	}
	rows.Close()
	meta := map[string]string{}
	rows, err = db.QueryContext(ctx, "SELECT k, v FROM schema_meta")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, nil, err
		}
		meta[k] = v
	}
	return applied, meta, rows.Err()
}

func schemaStatusOf(known []Migration, applied map[string]bool, meta map[string]string) SchemaStatus {
	st := SchemaStatus{Latest: BaselineMigration, MinBinary: meta[metaMinBinary]}
	fmt.Sscan(meta[metaRestoreGeneration], &st.RestoreGeneration)
	knownIDs := map[string]bool{BaselineMigration: true}
	for _, m := range known {
		knownIDs[m.ID] = true
		st.Latest = m.ID
		if !applied[m.ID] {
			st.Pending = append(st.Pending, m.ID)
			if m.Contract {
				st.PendingContract = append(st.PendingContract, m.ID)
			}
		}
	}
	for id := range applied {
		if id > st.Applied {
			st.Applied = id
		}
		if !knownIDs[id] {
			st.Ahead = append(st.Ahead, id)
		}
	}
	sort.Strings(st.Ahead)
	return st
}

// checkMinBinary refuses a binary older than the store's minimum.
func checkMinBinary(meta map[string]string, self string) error {
	min := meta[metaMinBinary]
	if min == "" || version.IsDev(self) {
		return nil
	}
	mv, err1 := version.Parse(min)
	sv, err2 := version.Parse(self)
	if err1 != nil || err2 != nil {
		return nil
	}
	if sv.Compare(mv) < 0 {
		return errSchemaTooNew{min: mv.String(), self: sv.String()}
	}
	return nil
}

// ensureSchemaTables creates the bookkeeping tables.
func (s *SQLStore) ensureSchemaTables(ctx context.Context) error {
	for _, stmt := range splitSQL(schemaMigrationsDDL) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w: %s", err, stmt)
		}
	}
	return nil
}

// applyMigrations records the baseline and applies every pending
// migration, each in its own transaction. Called by migrate() with the
// Postgres advisory lock held.
//
// Contract migrations run only with allowContract: while an older replica
// is live (it would break against the contracted schema), opening the
// store defers them, and an upgrade run (or `tw-mgmt migrate --apply`)
// applies them once every replica runs this version
// (ApplyDeferredContract).
func (s *SQLStore) applyMigrations(ctx context.Context, known []Migration, self string, allowContract bool) error {
	now := s.clockNow()
	if _, err := s.db.ExecContext(ctx, s.q("INSERT INTO schema_migrations (id, applied_at, applied_by) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING"),
		BaselineMigration, now, self); err != nil {
		return err
	}
	applied, _, err := readSchema(ctx, s.db)
	if err != nil {
		return err
	}
	for _, m := range known {
		if applied[m.ID] || m.Contract && !allowContract {
			continue
		}
		err := s.tx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range splitSQL(stripSQLComments(m.sql(s.d))) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("%w: %s", err, stmt)
				}
			}
			contract := 0
			if m.Contract {
				contract = 1
				if !version.IsDev(self) {
					sv, perr := version.Parse(self)
					if perr == nil {
						if _, err := tx.ExecContext(ctx, s.q("INSERT INTO schema_meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v=excluded.v"),
							metaMinBinary, sv.String()); err != nil {
							return err
						}
					}
				}
			}
			_, err := tx.ExecContext(ctx, s.q("INSERT INTO schema_migrations (id, applied_at, applied_by, contract) VALUES (?, ?, ?, ?)"),
				m.ID, now, self, contract)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", m.ID, err)
		}
	}
	return nil
}

func stripSQLComments(stmt string) string {
	var b strings.Builder
	for _, line := range strings.Split(stmt, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *SQLStore) clockNow() int64 {
	if s.clock != nil {
		return s.clock.Now().UnixMilli()
	}
	return localClock{}.Now().UnixMilli()
}

// SchemaStatus reports the store's schema against this binary's
// migrations.
func (s *SQLStore) SchemaStatus(ctx context.Context) (SchemaStatus, error) {
	applied, meta, err := readSchema(ctx, s.db)
	if err != nil {
		return SchemaStatus{}, err
	}
	return schemaStatusOf(Migrations(), applied, meta), nil
}

// CheckSchema opens dsn without migrating and reports what opening it
// would do (`tw-mgmt migrate --check`). A store that has never been
// opened by a migrations-aware tw-mgmt reports every migration pending.
func CheckSchema(ctx context.Context, dsn string) (SchemaStatus, error) {
	s, err := openSQL(ctx, dsn)
	if err != nil {
		return SchemaStatus{}, err
	}
	defer s.db.Close()
	applied, meta := map[string]bool{}, map[string]string{}
	var n int
	// The tables may not exist yet (a store from before migrations).
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&n); err == nil {
		if applied, meta, err = readSchema(ctx, s.db); err != nil {
			return SchemaStatus{}, err
		}
	}
	st := schemaStatusOf(Migrations(), applied, meta)
	if err := checkMinBinary(meta, StoreBinaryVersion); err != nil {
		return st, err
	}
	return st, nil
}

// StoreBinaryVersion is the version this process applies migrations as and
// is checked against the store's minimum. It is internal/version.Version;
// tests override it.
var StoreBinaryVersion = version.Version

// olderReplicaLive reports whether a replica on an older version than self
// was seen recently (contract migrations wait for it: applyMigrations).
func (s *SQLStore) olderReplicaLive(ctx context.Context, self string) bool {
	if version.IsDev(self) {
		return false
	}
	sv, err := version.Parse(self)
	if err != nil {
		return false
	}
	rows, err := s.db.QueryContext(ctx, s.q("SELECT version FROM replicas WHERE last_seen > ?"),
		time.Now().Add(-ReplicaLiveTTL).UnixMilli())
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) != nil || v == version.Dev {
			continue
		}
		if v == "" {
			v = version.Legacy
		}
		if ov, err := version.Parse(v); err == nil && ov.Compare(sv) < 0 {
			return true
		}
	}
	return false
}

// BackupSQLite writes a consistent copy of the SQLite store at dbPath to
// dst (VACUUM INTO; safe while tw-mgmt runs). tw upgrade calls it before
// it swaps binaries.
func BackupSQLite(ctx context.Context, dbPath, dst string) error {
	s, err := openSQL(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.db.Close()
	if s.d != dialectSQLite {
		return fmt.Errorf("BackupSQLite: %s is not a SQLite store", dbPath)
	}
	_, err = s.db.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}
