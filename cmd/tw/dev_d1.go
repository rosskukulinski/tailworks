package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
)

// D1 in tw dev (ADR-038): dev stores live in the in-memory mgmt, so every
// `tw dev` starts with empty databases. After each (re)deploy, dev
// applies each d1_databases entry's pending migrations (wrangler's
// migrations_dir layout), through the dev host's store admin op, as
// `wrangler d1 migrations apply --local` would.

// appD1Databases are the app's d1_databases, from its wrangler config as
// loaded (wrangler.jsonc, .json or .toml, with --env applied).
func appD1Databases(a *app) ([]bundle.D1Database, error) {
	b, err := json.Marshal(a.Wrangler)
	if err != nil {
		return nil, err
	}
	return bundle.D1Databases(b)
}

type devD1 struct {
	Binding  string   `json:"binding"`
	Database string   `json:"database"`
	Applied  []string `json:"applied"`
}

func devD1Migrate(ctx context.Context, h *hostd.Host, tenantID string, a *app) ([]devD1, error) {
	dbs, err := appD1Databases(a)
	if err != nil {
		return nil, err
	}
	var out []devD1
	for _, db := range dbs {
		done, err := d1mig.Apply(ctx, devD1Exec(h, tenantID, a.Name, db.DatabaseName), filepath.Join(a.Dir, db.MigrationsDir), db.MigrationsTable)
		if err != nil {
			return out, fmt.Errorf("d1 %s (%s): %w", db.Binding, db.DatabaseName, err)
		}
		out = append(out, devD1{Binding: db.Binding, Database: db.DatabaseName, Applied: done})
	}
	return out, nil
}

// devD1Exec runs batches on database of the dev workload, through the dev
// host's store admin op (also the dev console's reads, devconsole.go).
func devD1Exec(h *hostd.Host, tenantID, workload, database string) d1mig.Exec {
	return func(ctx context.Context, qs []d1mig.Query) ([]d1mig.Result, error) {
		arg, _ := json.Marshal(map[string]any{"queries": qs})
		b, err := h.StoreAdmin(ctx, tenantID, workload, "d1", database, "query", arg)
		if err != nil {
			return nil, err
		}
		var rs []d1mig.Result
		if json.Unmarshal(b, &rs) != nil {
			var one d1mig.Result
			json.Unmarshal(b, &one)
			return nil, fmt.Errorf("D1_ERROR: %s", one.Error)
		}
		return rs, nil
	}
}
