package main

// D1 (ADR-038), CLI side: `tw d1 …`, like `wrangler d1 …`. A database
// is the platform store (kind "d1") of a workload, named by the binding's
// database_name. create, ls and rm (or wrangler's list and delete) use
// mgmt's store routes; execute, export and migrations go through mgmt to
// the host running the workload
// (POST …/d1/{name}/query, GET …/d1/{name}/export), so the workload must
// be deployed. Migrations are wrangler's: numbered .sql files in the
// binding's migrations_dir, recorded in migrations_table (internal/d1mig).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/d1mig"
	"github.com/rosskukulinski/tailworks/mgmt"
)

const d1Usage = "d1 create <db> | d1 ls | d1 rm <db> [--yes] | d1 execute <db> [--command <sql> | --file <file>] | d1 export <db> [--output <file>] | d1 repair <db> --skip <seq> [--yes] | d1 migrations create <db> <message> | d1 migrations ls <db> | d1 migrations apply <db>   [--workload <workload>] [--tenant <tailnet>]"

type d1Result struct {
	Workload  string           `json:"workload"`
	TenantID  string           `json:"tenantID,omitempty"`
	Database  string           `json:"database,omitempty"`
	Databases []mgmt.StoreInfo `json:"databases,omitempty"`
	Deleted   bool             `json:"deleted,omitempty"`
	Results   []d1mig.Result   `json:"results,omitempty"`
	Pending   []string         `json:"pending,omitempty"`
	Applied   []string         `json:"applied,omitempty"`
	File      string           `json:"file,omitempty"`
	Bytes     int              `json:"bytes,omitempty"`

	// repair (d1repair.go): the database after the skipped entry.
	Repaired *mgmt.StoreInfo `json:"repaired,omitempty"`
	Skipped  int64           `json:"skipped,omitempty"`
}

// d1Verbs are tw d1's commands and how many arguments each takes.
var d1Verbs = map[string]int{
	"create":            1,
	"execute":           1,
	"export":            1,
	"ls":                0,
	"migrations apply":  1,
	"migrations create": 2,
	"migrations ls":     1,
	"repair":            1,
	"rm":                1,
}

// d1Opts are tw d1's flags. A verb registers only its own (d1Flags); the
// others keep their zero value.
type d1Opts struct {
	tenant, workload *string

	// execute
	command, file *string

	// export
	output *string

	// repair
	skip *int64

	// rm, repair
	yes *bool
}

// d1Flags registers verb's flags ("execute"; anyVerb: every verb's) on f.
func d1Flags(f *flags, verb string) *d1Opts {
	o := &d1Opts{command: new(string), file: new(string), output: new(string), skip: new(int64), yes: new(bool)}
	o.tenant = tenantFlag(f)
	o.workload = f.String("workload", "", "workload whose database it is (default: the app in the current directory, as tw deploy names it)")
	if onVerb(verb, "execute") {
		o.command = f.String("command", "", "SQL to run (one or more statements, run as one batch)")
		o.file = f.String("file", "", "a file of SQL to run as one batch (with neither flag, the SQL is read from stdin: tw d1 execute <db> < file.sql)")
	}
	if onVerb(verb, "export") {
		o.output = f.String("output", "", "write the SQL to this file (default: stdout)")
	}
	if onVerb(verb, "repair") {
		o.skip = f.Int64("skip", 0, "the journal entry to skip (the entry tw d1 ls shows the database degraded at)")
	}
	if onVerb(verb, "rm", "repair") {
		o.yes = yesFlag(f)
	}
	return o
}

func cmdD1(args []string) error {
	// The verb is one word, or two after "migrations"; flags may come
	// before it.
	sub, args, complete := scanVerb(args, func(f *flags) { d1Flags(f, anyVerb) }, func(first string) int {
		if first == "migrations" {
			return 2
		}
		return 1
	})
	n, ok := d1Verbs[sub]
	if complete && !ok {
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q", sub), "usage: tw "+usageOf("d1"))
	}
	if !complete && sub != "" {
		return needsSubcommand("d1")
	}
	f := newFlags("d1")
	o := d1Flags(f, sub)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if !complete {
		return needsSubcommand("d1")
	}
	if len(pos) != n {
		return fail("bad_request", fmt.Sprintf("tw d1 %s takes %d argument(s)", sub, n), "usage: tw "+usageOf("d1"))
	}

	// The app in the current directory names the workload and maps a
	// binding to its database and migrations.
	var dbs []bundle.D1Database
	appDir := "."
	a, appErr := loadApp(appDir, "")
	if appErr == nil {
		if dbs, err = appD1Databases(a); err != nil {
			return fail("bad_request", err.Error())
		}
	}
	var db *bundle.D1Database
	name := ""
	if len(pos) > 0 {
		bs := make([]bundle.StoreBinding, len(dbs))
		for i, d := range dbs {
			bs[i] = bundle.StoreBinding{Kind: "d1", Binding: d.Binding, Name: d.DatabaseName}
		}
		name = storeName(bs, pos[0])
		for i := range dbs {
			if dbs[i].DatabaseName == name {
				db = &dbs[i]
				break
			}
		}
		if !mgmt.ValidStoreName(name) {
			return fail("bad_request", fmt.Sprintf("bad database name %q ([A-Za-z0-9][A-Za-z0-9_.-]{0,63})", name))
		}
	}
	if strings.HasPrefix(sub, "migrations") && db == nil {
		return fail("bad_request", fmt.Sprintf("no d1_databases entry %q in ./wrangler.jsonc (its migrations_dir is where migrations live)", name)).
			withFix("cd <app dir> && tw d1 " + sub + " <database_name|binding> …")
	}
	if sub == "migrations create" {
		p, err := d1mig.Create(filepath.Join(a.Dir, db.MigrationsDir), pos[1], time.Now())
		if err != nil {
			return fail("bad_request", err.Error())
		}
		emit(f, d1Result{Database: name, File: p}, func() {
			fmt.Printf("✓ created %s\n", p)
		})
		return nil
	}

	workload := *o.workload
	if workload == "" {
		if appErr != nil {
			return appWorkloadErr(appDir, "d1 "+sub, appErr, "") // appworkload.go
		}
		workload = a.Name
	}
	if workloadLabel(workload) != workload {
		return fail("bad_request", fmt.Sprintf("workload %q is not a DNS label", workload))
	}
	execSQL := ""
	if sub == "execute" {
		if execSQL, err = d1ExecuteSQL(*o.command, *o.file); err != nil { // d1stdin.go
			return err
		}
	}
	if sub == "rm" {
		if err := confirmDestroy(f, *o.yes, "deleting database "+name+" deletes its data (its journal and key in mgmt)", name, "tw d1 rm "+name,
			"tw d1 export "+name+" --output backup.sql   (first, to keep a copy)"); err != nil {
			return err
		}
	}
	if sub == "repair" {
		if err := confirmD1Repair(f, *o.yes, name, *o.skip); err != nil {
			return err
		}
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*o.tenant)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := s.mgmt()
	wlBase := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload)
	storePath := wlBase + "/stores/d1/" + url.PathEscape(name)
	d1Path := wlBase + "/d1/" + url.PathEscape(name)
	hdr := map[string]string{"Tw-Actor": actor()}
	exec := func(ctx context.Context, qs []d1mig.Query) ([]d1mig.Result, error) {
		var rs []d1mig.Result
		err := c.DoHeaders(ctx, "POST", d1Path+"/query", map[string]any{"queries": qs}, &rs, hdr)
		return rs, err
	}
	res := d1Result{Workload: workload, TenantID: tn.TenantID, Database: name}

	switch sub {
	case "create":
		if err := c.DoHeaders(ctx, "PUT", storePath, nil, nil, hdr); err != nil {
			return err
		}
		emit(f, res, func() {
			fmt.Printf("✓ database %s of workload %s (tenant %s)\n", name, workload, tn.Tailnet)
			fmt.Printf("  bind it in wrangler.jsonc: \"d1_databases\": [{ \"binding\": \"DB\", \"database_name\": %q }]\n", name)
		})
	case "ls":
		items, err := listAll[mgmt.StoreInfo](ctx, c, wlBase+"/stores?kind=d1", 0)
		if err != nil {
			return err
		}
		res.Databases = items
		if res.Databases == nil {
			res.Databases = []mgmt.StoreInfo{}
		}
		emit(f, res, func() {
			if len(items) == 0 {
				fmt.Printf("no D1 databases for workload %s (tenant %s)\n", workload, tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %10s %12s  %s\n", "NAME", "WRITES", "JOURNAL", "UPDATED")
			for _, d := range items {
				fmt.Printf("%-32s %10d %12s  %s\n", tsafe(d.Name), d.Head, humanBytes(d.Bytes), time.UnixMilli(d.UpdatedAt).Local().Format("2006-01-02 15:04:05"))
				printStoreDegraded(d.Degraded, storeFix(d))
			}
		})
	case "rm":
		if err := c.DoHeaders(ctx, "DELETE", storePath, nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = true
		emit(f, res, func() {
			fmt.Printf("✓ database %s of workload %s deleted (tenant %s)\n", name, workload, tn.Tailnet)
		})
	case "execute":
		var qs []d1mig.Query
		for _, st := range d1mig.Split(execSQL) {
			qs = append(qs, d1mig.Query{SQL: st})
		}
		if len(qs) == 0 {
			return fail("bad_request", "no SQL statement to run: pass --command <sql>, --file <file>, or SQL on stdin")
		}
		rs, err := exec(ctx, qs)
		if err != nil {
			return d1Fix(err, workload)
		}
		res.Results = rs
		emit(f, res, func() { printD1Results(rs) })
	case "export":
		var raw []byte
		if err := c.DoHeaders(ctx, "GET", d1Path+"/export", nil, &raw, hdr); err != nil {
			return d1Fix(err, workload)
		}
		res.Bytes = len(raw)
		if *o.output == "" {
			os.Stdout.Write(raw)
			return nil
		}
		if err := os.WriteFile(*o.output, raw, 0o600); err != nil {
			return fail("bad_request", err.Error())
		}
		res.File = *o.output
		emit(f, res, func() { fmt.Printf("✓ exported %s (%s) to %s\n", name, humanBytes(int64(len(raw))), *o.output) })
	case "repair":
		if err := d1Repair(ctx, c, f, &res, storePath, *o.skip, hdr); err != nil {
			return err
		}
	case "migrations ls":
		pend, err := d1mig.Pending(ctx, exec, filepath.Join(a.Dir, db.MigrationsDir), db.MigrationsTable)
		if err != nil {
			return d1Fix(err, workload)
		}
		res.Pending = []string{}
		for _, m := range pend {
			res.Pending = append(res.Pending, m.Name)
		}
		emit(f, res, func() {
			if len(pend) == 0 {
				fmt.Printf("✓ no migrations to apply to %s\n", name)
				return
			}
			fmt.Printf("migrations to apply to %s:\n", name)
			for _, m := range pend {
				fmt.Println("  " + m.Name)
			}
		})
	case "migrations apply":
		done, err := d1mig.Apply(ctx, exec, filepath.Join(a.Dir, db.MigrationsDir), db.MigrationsTable)
		res.Applied = done
		for _, m := range done {
			fmt.Fprintf(os.Stderr, "✓ applied %s\n", m)
		}
		if err != nil {
			return d1Fix(err, workload)
		}
		emit(f, res, func() {
			if len(done) == 0 {
				fmt.Printf("✓ no migrations to apply to %s\n", name)
			}
		})
	}
	return nil
}

// d1Fix adds the usual way out to a workload that isn't running.
func d1Fix(err error, workload string) error {
	if e, ok := mgmtapi.AsError(err); ok && e.Code == "unknown_workload" {
		return fail(e.Code, e.Message).withFix("tw deploy   (a D1 database is reached through the running workload " + workload + " that binds it)")
	}
	return err
}

func printD1Results(rs []d1mig.Result) {
	for i, r := range rs {
		cols := tsafeAll(r.Results.Columns)
		if len(rs) > 1 {
			fmt.Printf("-- statement %d: ", i+1)
		}
		if len(cols) == 0 {
			fmt.Printf("✓ %v change(s)\n", r.Meta["changes"])
			continue
		}
		if len(rs) > 1 {
			fmt.Printf("%d row(s)\n", len(r.Results.Rows))
		}
		w := make([]int, len(cols))
		cells := make([][]string, len(r.Results.Rows))
		for j, c := range cols {
			w[j] = len(c)
		}
		for k, row := range r.Results.Rows {
			cells[k] = make([]string, len(cols))
			for j := range cols {
				var v any
				if j < len(row) {
					v = row[j]
				}
				s := "NULL"
				switch x := v.(type) {
				case nil:
				case string:
					s = tsafe(x) // tenant data
				case []any:
					s = fmt.Sprintf("<blob %d bytes>", len(x))
				default:
					b, _ := json.Marshal(x)
					s = tsafe(string(b))
				}
				cells[k][j] = s
				w[j] = max(w[j], len(s))
			}
		}
		line := func(vals []string) {
			for j, v := range vals {
				if j > 0 {
					fmt.Print("  ")
				}
				fmt.Printf("%-*s", w[j], v)
			}
			fmt.Println()
		}
		line(cols)
		for _, row := range cells {
			line(row)
		}
	}
}
