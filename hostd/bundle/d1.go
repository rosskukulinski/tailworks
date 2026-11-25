package bundle

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// D1 (ADR-038): wrangler.jsonc "d1_databases" entries are platform stores
// of kind "d1", one per database_name. The migration settings are read by
// the CLI (`tw d1 migrations …`), not by hostd.

// D1Database is one d1_databases entry.
type D1Database struct {
	Binding      string `json:"binding"`
	DatabaseName string `json:"database_name"`
	// DatabaseID is wrangler's database UUID. Tailworks names a database by
	// DatabaseName within the workload, so it is accepted and ignored.
	DatabaseID        string `json:"database_id,omitempty"`
	PreviewDatabaseID string `json:"preview_database_id,omitempty"`
	// MigrationsDir is relative to the app directory (default "migrations").
	MigrationsDir string `json:"migrations_dir,omitempty"`
	// MigrationsTable records applied migrations (default "d1_migrations").
	MigrationsTable string `json:"migrations_table,omitempty"`
}

// D1 migration defaults (wrangler's).
const (
	D1DefaultMigrationsDir   = "migrations"
	D1DefaultMigrationsTable = "d1_migrations"
)

// ParseD1Databases reads a wrangler.jsonc d1_databases value, filling in
// the migration defaults.
func ParseD1Databases(raw json.RawMessage) ([]D1Database, error) {
	var in []D1Database
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("want an array of {binding, database_name, …}: %w", err)
	}
	for i := range in {
		d := &in[i]
		if d.Binding == "" {
			return nil, fmt.Errorf("entry %d has no binding", i)
		}
		if d.DatabaseName == "" {
			return nil, fmt.Errorf("binding %s has no database_name", d.Binding)
		}
		if d.MigrationsDir == "" {
			d.MigrationsDir = D1DefaultMigrationsDir
		}
		if filepath.IsAbs(d.MigrationsDir) || strings.HasPrefix(filepath.Clean(d.MigrationsDir), "..") {
			return nil, fmt.Errorf("binding %s: migrations_dir must be inside the app directory", d.Binding)
		}
		if d.MigrationsTable == "" {
			d.MigrationsTable = D1DefaultMigrationsTable
		}
		if !identRE.MatchString(d.MigrationsTable) || strings.HasPrefix(d.MigrationsTable, "_cf_") {
			return nil, fmt.Errorf("binding %s: bad migrations_table %q", d.Binding, d.MigrationsTable)
		}
	}
	return in, nil
}

// D1Databases reads the d1_databases of a wrangler.jsonc (JSONC allowed);
// none is an empty list.
func D1Databases(wranglerJSONC []byte) ([]D1Database, error) {
	var top struct {
		D1 json.RawMessage `json:"d1_databases"`
	}
	if err := json.Unmarshal(StripJSONC(wranglerJSONC), &top); err != nil {
		return nil, fmt.Errorf("wrangler.jsonc: %w", err)
	}
	if len(top.D1) == 0 || string(top.D1) == "null" {
		return nil, nil
	}
	ds, err := ParseD1Databases(top.D1)
	if err != nil {
		return nil, fmt.Errorf("wrangler.jsonc d1_databases: %w", err)
	}
	return ds, nil
}

func init() {
	RegisterStoreKind(StoreKind{Kind: "d1", WranglerKey: "d1_databases",
		Parse: func(raw json.RawMessage) ([]StoreBinding, error) {
			ds, err := ParseD1Databases(raw)
			if err != nil {
				return nil, err
			}
			out := make([]StoreBinding, len(ds))
			for i, d := range ds {
				out[i] = StoreBinding{Binding: d.Binding, Name: d.DatabaseName}
			}
			return out, nil
		}})
}
