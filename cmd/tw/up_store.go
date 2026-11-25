package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/mgmtd"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw up's store and custody flags (issue #120): mgmt on a Postgres store
// (--db-file, or --db) and setec custody (--custody setec --setec-url),
// passed to the embedded mgmt (single layout) or to tw-mgmt (separate).
// They are kept in tw.json like every other tw up flag (up_saved.go).
//
// This runs ONE mgmt replica. Several replicas behind svc:tw-mgmt (HA) run
// tw-mgmt directly on each machine (docs/postgres.md); tw up does not
// host the Service.
type storeFlags struct {
	db, dbFile, custody, setecURL *string
}

func addStoreFlags(f *flags) storeFlags {
	f.group(upGroupStore,
		"db",
		"db-file",
		"custody",
		"setec-url",
	)
	return storeFlags{
		db:       f.String("db", "", "mgmt's store: a postgres://… DSN (saved in tw.json; prefer --db-file for a DSN with a password). Default: SQLite in <state>/mgmt/mgmt.db. Move an existing install first: tw-mgmt migrate --from sqlite --to-file <file>"),
		dbFile:   f.String("db-file", "", "read mgmt's store DSN from this 0600 file at every start (keeps a Postgres password out of tw.json and argv)"),
		custody:  f.String("custody", "", "where mgmt keeps secrets: file (default: AES-GCM in <state>/mgmt/custody) or setec (with --setec-url; what several mgmt replicas on one store need)"),
		setecURL: f.String("setec-url", "", "--custody setec: the setec server URL"),
	}
}

// validate checks the flags on their own (tw up runs it before it
// changes anything) and returns the store DSN file as an absolute path.
func (sf storeFlags) validate() (dbFile string, err error) {
	db, dbFile := strings.TrimSpace(*sf.db), strings.TrimSpace(*sf.dbFile)
	if db != "" && dbFile != "" {
		return "", fail("bad_request", "give the store as --db or --db-file, not both")
	}
	if db != "" && !isPostgres(db) {
		return "", fail("bad_request", "--db takes a postgres://… DSN (the default SQLite store needs no flag)")
	}
	if dbFile != "" {
		abs, err := filepath.Abs(dbFile)
		if err != nil {
			return "", err
		}
		dbFile = abs
		b, err := os.ReadFile(dbFile)
		if err != nil {
			return "", fail("bad_request", "--db-file: "+err.Error())
		}
		if !isPostgres(strings.TrimSpace(string(b))) {
			return "", fail("bad_request", "--db-file "+dbFile+" must hold a postgres://… DSN")
		}
		if fi, err := os.Stat(dbFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
			return "", fail("credential_file_insecure", "--db-file "+dbFile+" is readable by other users").withFix("chmod 600 " + dbFile)
		}
	}
	switch *sf.custody {
	case "", "file":
		if *sf.setecURL != "" {
			return "", fail("bad_request", "--setec-url needs --custody setec")
		}
	case "setec":
		if *sf.setecURL == "" {
			return "", fail("bad_request", "--custody setec needs --setec-url <url>")
		}
	default:
		return "", fail("bad_request", "--custody is file or setec")
	}
	return dbFile, nil
}

// apply validates the flags and records them in s.Cfg. Switching an
// install that has a SQLite store to Postgres needs the data moved first:
// a target no tw-mgmt ever migrated is refused. Custody never changes
// under an install that has secrets.
func (sf storeFlags) apply(s *state) error {
	c := &s.Cfg
	dbFile, err := sf.validate()
	if err != nil {
		return err
	}
	db := strings.TrimSpace(*sf.db)
	newCustody := *sf.custody
	if newCustody == "file" {
		newCustody = ""
	}
	if newCustody != c.Custody {
		secrets, _ := os.ReadDir(filepath.Join(s.Dir, "mgmt", "custody", "secrets"))
		if c.Custody == "setec" || len(secrets) > 0 {
			move := "or start a new install (another --state-dir) with --custody " + custodyName(newCustody) + ", and move workloads to it"
			if newCustody == "setec" {
				move = "or move file custody to setec: tw down, then " + custodyMigrateCmd(*sf.setecURL, s.Dir) + ", then tw up (docs/postgres.md)" // custody.go
			}
			return fail("state_conflict", "this install keeps its secrets and store keys in "+custodyName(c.Custody)+" custody; tw up can't move them to "+custodyName(newCustody)+" custody",
				"the store's data is sealed with keys in custody, so switching would make it unreadable").
				withFix("keep the custody this install was set up with (leave --custody out)",
					move)
		}
	}
	wasSQLite := c.storeIsSQLite()
	next := Config{DB: db, DBFile: dbFile}
	if wasSQLite && !next.storeIsSQLite() && fileExists(filepath.Join(s.Dir, "mgmt", "mgmt.db")) {
		dsn, err := next.storeDSN(s.Dir)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := mgmt.CheckSchema(ctx, dsn)
		if err != nil {
			return fail("bad_request", "the Postgres store: "+err.Error())
		}
		if st.Applied == "" {
			return fail("state_conflict", "this install keeps its data in "+filepath.Join(s.Dir, "mgmt", "mgmt.db")+", and the Postgres store is empty",
				"starting on it would lose every tenant, deployment and store").
				withFix("tw down", "tw-mgmt migrate --from sqlite --state-dir "+filepath.Join(s.Dir, "mgmt")+" --to-file <file with the DSN>",
					"then tw up --db-file <file with the DSN>   (docs/postgres.md)")
		}
	}
	c.DB, c.DBFile, c.Custody, c.SetecURL = db, dbFile, newCustody, *sf.setecURL
	return nil
}

func custodyName(k string) string {
	if k == "" {
		return "file"
	}
	return k
}

func isPostgres(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// storeIsSQLite reports whether mgmt runs on <state>/mgmt/mgmt.db.
func (c Config) storeIsSQLite() bool { return c.DB == "" && c.DBFile == "" }

// storeDSN is the DSN mgmt opens.
func (c Config) storeDSN(stateDir string) (string, error) {
	switch {
	case c.DBFile != "":
		b, err := os.ReadFile(c.DBFile)
		return strings.TrimSpace(string(b)), err
	case c.DB != "":
		return c.DB, nil
	}
	return filepath.Join(stateDir, "mgmt", "mgmt.db"), nil
}

// storeMgmtArgs are tw-mgmt's store and custody flags (separate layout).
func (c Config) storeMgmtArgs() []string {
	var a []string
	switch {
	case c.DBFile != "":
		a = append(a, "-db-file", c.DBFile)
	case c.DB != "":
		a = append(a, "-db", c.DB)
	}
	if c.Custody == "setec" {
		a = append(a, "-custody", "setec", "-setec-url", c.SetecURL)
	}
	return a
}

// storeOptions sets the embedded mgmt's store and custody (single layout).
func (c Config) storeOptions(o *mgmtd.Options) {
	o.DB, o.DBFile = c.DB, c.DBFile
	if c.Custody == "setec" {
		o.Custody, o.SetecURL = "setec", c.SetecURL
	}
}

// storeLabel describes the store and custody for tw up's summary.
func (c Config) storeLabel(stateDir string) string {
	store := "leases in SQLite (" + filepath.Join(stateDir, "mgmt", "mgmt.db") + ")"
	if !c.storeIsSQLite() {
		store = "store: Postgres"
		if c.DBFile != "" {
			store += " (DSN in " + c.DBFile + ")"
		}
	}
	custody := "custody: file (homelab mode)"
	if c.Custody == "setec" {
		custody = "custody: setec (" + c.SetecURL + ")"
	}
	return store + " · " + custody
}
