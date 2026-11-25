package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CopyReport is what CopySQLiteStore did (`tw-mgmt migrate --from sqlite
// --to <dsn>`).
type CopyReport struct {
	From string `json:"from"`
	To   string `json:"to"` // password redacted
	// Rows copied per table.
	Tables map[string]int64 `json:"tables"`
	// Schema is the target's schema after the copy.
	Schema SchemaStatus `json:"schema"`
	// RestoreGeneration is the target's after its restore fence.
	RestoreGeneration int64 `json:"restoreGeneration"`
}

// schemaBookkeeping are the tables the target writes for itself when it is
// opened; schema_meta is copied key by key (copyMeta).
var schemaBookkeeping = map[string]bool{"schema_migrations": true, "schema_meta": true}

// CopySQLiteStore copies the SQLite store at src into the empty store at
// dst (Postgres, or another SQLite file), then runs the restore fence on
// dst. It is how an install moves from SQLite to Postgres.
//
//   - src is never written: the copy reads a VACUUM INTO snapshot, brought
//     to this binary's schema (contract migrations included) the way
//     tw-mgmt would on start.
//   - It refuses while a tw-mgmt replica is live on src (writes after the
//     snapshot would be lost) and when dst holds any data.
//   - Every row of every table is copied in one transaction on dst, as
//     stored: journal entries stay sealed under their custody keys, the
//     audit chain keeps its hashes, custody_names its index. So dst works
//     with the same custody (and ID secret) as src.
//   - The restore fence then moves every lease and deployment epoch past
//     any a host could hold from src.
func CopySQLiteStore(ctx context.Context, src, dst string) (CopyReport, error) {
	rep := CopyReport{From: src, To: redactDSN(dst), Tables: map[string]int64{}}
	src = strings.TrimPrefix(src, "sqlite:")
	if isPostgresDSN(src) {
		return rep, errors.New("--from must be a SQLite store (a file path)")
	}
	if _, err := os.Stat(src); err != nil {
		return rep, fmt.Errorf("source store: %w", err)
	}
	if strings.TrimPrefix(dst, "sqlite:") == src {
		return rep, errors.New("the target is the source store")
	}
	if err := refuseLiveReplicas(ctx, src); err != nil {
		return rep, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(src), ".tw-migrate-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "mgmt.db")
	if err := BackupSQLite(ctx, src, snap); err != nil {
		return rep, fmt.Errorf("snapshot: %w", err)
	}
	from, err := openAtLatest(ctx, snap)
	if err != nil {
		return rep, fmt.Errorf("source store: %w", err)
	}
	defer from.Close()
	to, err := openAtLatest(ctx, dst)
	if err != nil {
		return rep, fmt.Errorf("target store: %w", err)
	}
	defer to.Close()

	srcTables, err := from.tableColumns(ctx)
	if err != nil {
		return rep, err
	}
	dstTables, err := to.tableColumns(ctx)
	if err != nil {
		return rep, err
	}
	names := make([]string, 0, len(srcTables))
	for t := range srcTables {
		if !schemaBookkeeping[t] {
			names = append(names, t)
		}
	}
	sort.Strings(names)
	for _, t := range names {
		dc, ok := dstTables[t]
		if !ok {
			return rep, fmt.Errorf("table %s is not in the target's schema", t)
		}
		for _, c := range srcTables[t] {
			if _, ok := dc.kind(c.name); !ok {
				return rep, fmt.Errorf("column %s.%s is not in the target's schema", t, c.name)
			}
		}
		var n int64
		if err := to.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return rep, err
		}
		if n > 0 {
			return rep, fmt.Errorf("the target store is not empty (%s has %d rows): migrate into a new, empty database", t, n)
		}
	}
	err = to.tx(ctx, func(tx *sql.Tx) error {
		if to.d == dialectPostgres {
			// One long transaction: don't let the session limit (S6) abort it
			// between batches.
			if _, err := tx.ExecContext(ctx, "SET LOCAL idle_in_transaction_session_timeout = 0"); err != nil {
				return err
			}
		}
		for _, t := range names {
			n, err := copyTable(ctx, from, to, tx, t, srcTables[t], dstTables[t])
			if err != nil {
				return fmt.Errorf("copy %s: %w", t, err)
			}
			rep.Tables[t] = n
		}
		return copyMeta(ctx, from, to, tx)
	})
	if err != nil {
		return rep, err
	}
	if rep.RestoreGeneration, err = to.RestoreFence(ctx); err != nil {
		return rep, fmt.Errorf("restore fence: %w", err)
	}
	rep.Schema, err = to.SchemaStatus(ctx)
	return rep, err
}

// openAtLatest opens a store and applies every migration this binary
// knows, contract ones included; a store a newer tw-mgmt migrated is
// refused (its schema may have columns this one would drop).
func openAtLatest(ctx context.Context, dsn string) (*SQLStore, error) {
	s, err := OpenStore(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := s.ApplyDeferredContract(ctx); err != nil {
		s.Close()
		return nil, err
	}
	st, err := s.SchemaStatus(ctx)
	if err == nil && len(st.Ahead) > 0 {
		err = fmt.Errorf("version_skew: the store has migrations this tw-mgmt doesn't know (%s): run the newest tw-mgmt's migrate", strings.Join(st.Ahead, ", "))
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// refuseLiveReplicas refuses a store a tw-mgmt replica is serving.
func refuseLiveReplicas(ctx context.Context, path string) error {
	s, err := openSQL(ctx, path)
	if err != nil {
		return err
	}
	defer s.db.Close()
	// id and last_seen only: a store from an older tw-mgmt may lack the
	// newer columns.
	var rs []Replica
	if rows, err := s.db.QueryContext(ctx, "SELECT id, last_seen FROM replicas"); err == nil {
		for rows.Next() {
			var r Replica
			if err := rows.Scan(&r.ID, &r.LastSeen); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		rows.Close()
	}
	now := time.Now()
	for _, r := range rs {
		if r.LastSeen != 0 && now.Sub(time.UnixMilli(r.LastSeen)) < ReplicaLiveTTL {
			return fmt.Errorf("tw-mgmt replica %s is serving %s (seen %v ago): stop it first (tw down), so no write lands after the copy",
				r.ID, path, now.Sub(time.UnixMilli(r.LastSeen)).Round(time.Second))
		}
	}
	return nil
}

// colKind is how a column's values are converted for the target.
type colKind int

const (
	kindOther colKind = iota
	kindText
	kindInt
	kindFloat
	kindBlob
	kindBool
)

type column struct {
	name string
	kind colKind
}

type columns []column

func (cs columns) kind(name string) (colKind, bool) {
	for _, c := range cs {
		if c.name == name {
			return c.kind, true
		}
	}
	return kindOther, false
}

func kindOf(typ string) colKind {
	t := strings.ToLower(typ)
	switch {
	case strings.Contains(t, "bool"):
		return kindBool
	case strings.Contains(t, "int"):
		return kindInt
	case strings.Contains(t, "char"), strings.Contains(t, "text"), strings.Contains(t, "clob"):
		return kindText
	case strings.Contains(t, "blob"), strings.Contains(t, "bytea"):
		return kindBlob
	case strings.Contains(t, "real"), strings.Contains(t, "floa"), strings.Contains(t, "doub"), strings.Contains(t, "numeric"):
		return kindFloat
	}
	return kindOther
}

// tableColumns lists the store's tables and their columns.
func (s *SQLStore) tableColumns(ctx context.Context) (map[string]columns, error) {
	out := map[string]columns{}
	if s.d == dialectPostgres {
		rows, err := s.db.QueryContext(ctx, `SELECT c.table_name, c.column_name, c.data_type FROM information_schema.columns c
			JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
			WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE' ORDER BY c.table_name, c.ordinal_position`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var t, c, typ string
			if err := rows.Scan(&t, &c, &typ); err != nil {
				return nil, err
			}
			out[t] = append(out[t], column{c, kindOf(typ)})
		}
		return out, rows.Err()
	}
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()
	for _, t := range tables {
		rows, err := s.db.QueryContext(ctx, "SELECT name, type FROM pragma_table_info(?) ORDER BY cid", t)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c, typ string
			if err := rows.Scan(&c, &typ); err != nil {
				rows.Close()
				return nil, err
			}
			out[t] = append(out[t], column{c, kindOf(typ)})
		}
		rows.Close()
	}
	return out, nil
}

// copyBatchBytes bounds the values one INSERT carries (journal entries
// can be large); copyBatchParams stays under Postgres' 65535 parameters.
const (
	copyBatchBytes  = 8 << 20
	copyBatchParams = 30000
	copyBatchRows   = 500
)

func copyTable(ctx context.Context, from, to *SQLStore, tx *sql.Tx, table string, src, dst columns) (int64, error) {
	names := make([]string, len(src))
	kinds := make([]colKind, len(src))
	for i, c := range src {
		names[i] = c.name
		kinds[i], _ = dst.kind(c.name)
	}
	rows, err := from.db.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	maxRows := max(1, min(copyBatchRows, copyBatchParams/len(names)))
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
	var (
		batch []any
		nrows int
		bytes int
		total int64
	)
	flush := func() error {
		if nrows == 0 {
			return nil
		}
		q := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES " + strings.TrimSuffix(strings.Repeat(one+",", nrows), ",")
		if _, err := tx.ExecContext(ctx, to.q(q), batch...); err != nil {
			return err
		}
		total += int64(nrows)
		batch, nrows, bytes = batch[:0], 0, 0
		return nil
	}
	vals := make([]any, len(names))
	ptrs := make([]any, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return total, err
		}
		for i, v := range vals {
			cv, err := convertValue(v, kinds[i])
			if err != nil {
				return total, fmt.Errorf("column %s: %w", names[i], err)
			}
			switch b := cv.(type) {
			case []byte:
				bytes += len(b)
			case string:
				bytes += len(b)
			}
			batch = append(batch, cv)
		}
		nrows++
		if nrows >= maxRows || bytes >= copyBatchBytes {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	return total, flush()
}

// convertValue turns a value as SQLite returned it into one the target
// column takes (SQLite is dynamically typed; Postgres is not).
func convertValue(v any, k colKind) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch k {
	case kindText:
		switch x := v.(type) {
		case []byte:
			return string(x), nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64), nil
		}
	case kindInt:
		switch x := v.(type) {
		case string:
			return strconv.ParseInt(x, 10, 64)
		case []byte:
			return strconv.ParseInt(string(x), 10, 64)
		case float64:
			if x != float64(int64(x)) {
				return nil, fmt.Errorf("%v is not an integer", x)
			}
			return int64(x), nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
	case kindFloat:
		switch x := v.(type) {
		case int64:
			return float64(x), nil
		case string:
			return strconv.ParseFloat(x, 64)
		}
	case kindBlob:
		if x, ok := v.(string); ok {
			return []byte(x), nil
		}
	case kindBool:
		switch x := v.(type) {
		case int64:
			return x != 0, nil
		case string:
			return strconv.ParseBool(x)
		}
	}
	if t, ok := v.(time.Time); ok { // never written by tailworks; keep it exact
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	return v, nil
}

// copyMeta carries the source's schema_meta (restore generation, minimum
// binary) over, so the fence that follows counts on from it.
func copyMeta(ctx context.Context, from, to *SQLStore, tx *sql.Tx) error {
	_, meta, err := readSchema(ctx, from.db)
	if err != nil {
		return err
	}
	for _, k := range sortedMetaKeys(meta) {
		if _, err := tx.ExecContext(ctx, to.q("INSERT INTO schema_meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v=excluded.v"), k, meta[k]); err != nil {
			return err
		}
	}
	return nil
}

func sortedMetaKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// redactDSN hides a DSN's password.
func redactDSN(dsn string) string {
	if !isPostgresDSN(dsn) {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "postgres://…"
	}
	return u.Redacted()
}
