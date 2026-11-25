package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// Store maintenance subcommands (ADR-041 §3, §8):
//
//	tw-mgmt migrate --check  [-state-dir D | -db DSN | -db-file F] [-json]
//	tw-mgmt migrate --apply  …   apply pending migrations, contract ones included, then exit
//	tw-mgmt migrate --from sqlite --to <dsn> | --to-file F   copy a SQLite store into an empty one (Postgres), then fence it
//	tw-mgmt restore-fence    …   after restoring the store from a backup: lease epochs jump
//
// They work on the store directly and never serve; run them with every
// replica stopped (--apply on a contract migration) or at any time (--check).

func runMaintenance(args []string) (bool, error) {
	if len(args) == 0 || (args[0] != "migrate" && args[0] != "restore-fence") {
		return false, nil
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("tw-mgmt "+args[0], flag.ContinueOnError)
	stateDir := fs.String("state-dir", filepath.Join(home, ".tailworks", "mgmt"), "state directory (the default SQLite store is <state-dir>/mgmt.db)")
	dsn := fs.String("db", "", "store: SQLite path or postgres://… DSN (default <state-dir>/mgmt.db)")
	dsnFile := fs.String("db-file", "", "read the store DSN from this file")
	check := fs.Bool("check", false, "migrate: report what opening the store would do; change nothing")
	apply := fs.Bool("apply", false, "migrate: apply every pending migration, contract migrations included (stop the other replicas first)")
	from := fs.String("from", "", "migrate: copy this SQLite store (\"sqlite\" = -db or <state-dir>/mgmt.db, or a file path) into --to; stop tw-mgmt first")
	to := fs.String("to", "", "migrate --from: the empty target store (postgres://… DSN)")
	toFile := fs.String("to-file", "", "migrate --from: read the target DSN from this file (keeps a Postgres password off argv)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return true, err
	}
	d := *dsn
	if *dsnFile != "" {
		b, err := os.ReadFile(*dsnFile)
		if err != nil {
			return true, err
		}
		d = strings.TrimSpace(string(b))
	}
	if d == "" {
		d = filepath.Join(*stateDir, "mgmt.db")
	}
	timeout := 5 * time.Minute
	if *from != "" {
		timeout = 12 * time.Hour // a copy reads and writes every row
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := func(v any, human func()) {
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(v)
			return
		}
		human()
	}
	switch args[0] {
	case "migrate":
		if *from != "" || *to != "" || *toFile != "" {
			if *check || *apply {
				return true, fmt.Errorf("migrate: --from copies a store; don't combine it with --check or --apply")
			}
			return true, copyStore(ctx, *from, d, *to, *toFile, out)
		}
		if *check == *apply {
			return true, fmt.Errorf("migrate: pass exactly one of --check or --apply")
		}
		if *check {
			st, err := mgmt.CheckSchema(ctx, d)
			out(st, func() { printSchema(st) })
			return true, err
		}
		s, err := mgmt.OpenStore(ctx, d) // applies expand migrations
		if err != nil {
			return true, err
		}
		defer s.Close()
		applied, err := s.ApplyDeferredContract(ctx)
		if err != nil {
			return true, err
		}
		st, err := s.SchemaStatus(ctx)
		if err != nil {
			return true, err
		}
		out(map[string]any{"schema": st, "appliedContract": applied}, func() {
			printSchema(st)
			if len(applied) > 0 {
				fmt.Println("applied contract migrations:", strings.Join(applied, ", "))
			}
		})
		return true, nil
	default: // restore-fence: no migration (the restored store keeps its schema)
		gen, err := mgmt.RestoreFenceDSN(ctx, d)
		if err != nil {
			return true, err
		}
		out(map[string]any{"restoreGeneration": gen, "epochJump": mgmt.RestoreEpochJump}, func() {
			fmt.Printf("restore fence: every lease epoch moved up by %d (restore generation %d)\n", mgmt.RestoreEpochJump, gen)
		})
		return true, nil
	}
}

// copyStore is migrate --from: the SQLite store into an empty target, in
// a restore fence (mgmt.CopySQLiteStore).
func copyStore(ctx context.Context, from, dflt, to, toFile string, out func(any, func())) error {
	src := from
	if src == "sqlite" {
		src = dflt
	}
	if src == "" {
		return fmt.Errorf("migrate: --from sqlite (or a SQLite file path) is required with --to")
	}
	if toFile != "" {
		b, err := os.ReadFile(toFile)
		if err != nil {
			return err
		}
		to = strings.TrimSpace(string(b))
	}
	if to == "" {
		return fmt.Errorf("migrate --from: give the target with --to <dsn> or --to-file <file>")
	}
	rep, err := mgmt.CopySQLiteStore(ctx, src, to)
	if err != nil {
		return err
	}
	out(rep, func() {
		var rows int64
		for _, n := range rep.Tables {
			rows += n
		}
		fmt.Printf("copied %s → %s: %d rows in %d tables\n", rep.From, rep.To, rows, len(rep.Tables))
		printSchema(rep.Schema)
		fmt.Printf("restore fence: every lease epoch moved up by %d (restore generation %d)\n", mgmt.RestoreEpochJump, rep.RestoreGeneration)
		fmt.Println("next: start tw-mgmt with -db-file <file with the DSN> (tw up --db-file <file>), the same custody and state dir; keep the SQLite file until it serves")
	})
	return nil
}

func printSchema(st mgmt.SchemaStatus) {
	fmt.Printf("schema: applied %s, this tw-mgmt knows up to %s\n", st.Applied, st.Latest)
	if len(st.Pending) > 0 {
		fmt.Println("pending:", strings.Join(st.Pending, ", "))
	}
	if len(st.PendingContract) > 0 {
		fmt.Println("pending contract (applied once every replica runs this version, or with migrate --apply):", strings.Join(st.PendingContract, ", "))
	}
	if len(st.Ahead) > 0 {
		fmt.Println("ahead (applied by a newer tw-mgmt):", strings.Join(st.Ahead, ", "))
	}
	if st.MinBinary != "" {
		fmt.Println("minimum tw-mgmt:", st.MinBinary)
	}
}
