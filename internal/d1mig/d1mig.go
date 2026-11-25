// Package d1mig is D1's migration workflow (ADR-038), compatible with
// wrangler's: numbered .sql files in the database's migrations_dir
// (`0001_create_users.sql`), applied in name order, each recorded by file
// name in the migrations table (default d1_migrations, wrangler's schema).
// A migration is applied as one batch together with its record, so it
// lands whole or not at all.
//
// The package is transport-free: an Exec runs a batch against the
// database (the CLI goes through mgmt's API, tests through hostd).
package d1mig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Query is one statement of a batch.
type Query struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

// Result is one statement's result (D1's wire shape, ROWS_AND_COLUMNS).
type Result struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Results struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	} `json:"results"`
	Meta map[string]any `json:"meta,omitempty"`
}

// Exec runs queries as one atomic batch. A failed batch is an error.
type Exec func(ctx context.Context, queries []Query) ([]Result, error)

// Migration is one migration file.
type Migration struct {
	Name string `json:"name"` // the file name, as recorded in the table
	Path string `json:"-"`
}

var tableRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// createTable is wrangler's migrations table.
func createTable(table string) string {
	return "CREATE TABLE IF NOT EXISTS " + table + `(
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT UNIQUE,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);`
}

func checkTable(table string) error {
	if !tableRE.MatchString(table) {
		return fmt.Errorf("d1: bad migrations table name %q", table)
	}
	return nil
}

// List returns the migration files in dir (*.sql), in name order. A
// missing dir has none.
func List(dir string) ([]Migration, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, Migration{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Applied returns the names recorded in the migrations table, creating
// the table if it doesn't exist (as wrangler does).
func Applied(ctx context.Context, exec Exec, table string) ([]string, error) {
	if err := checkTable(table); err != nil {
		return nil, err
	}
	rs, err := exec(ctx, []Query{{SQL: createTable(table)}, {SQL: "SELECT name FROM " + table + " ORDER BY id"}})
	if err != nil {
		return nil, err
	}
	if len(rs) != 2 {
		return nil, fmt.Errorf("d1: listing applied migrations: %d results", len(rs))
	}
	var names []string
	for _, r := range rs[1].Results.Rows {
		if len(r) > 0 {
			if s, ok := r[0].(string); ok {
				names = append(names, s)
			}
		}
	}
	return names, nil
}

// Pending returns the migrations in dir not recorded in the table.
func Pending(ctx context.Context, exec Exec, dir, table string) ([]Migration, error) {
	all, err := List(dir)
	if err != nil {
		return nil, err
	}
	done, err := Applied(ctx, exec, table)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, n := range done {
		have[n] = true
	}
	var out []Migration
	for _, m := range all {
		if !have[m.Name] {
			out = append(out, m)
		}
	}
	return out, nil
}

// Statements is one migration as the batch that applies it: its
// statements, then the INSERT that records it.
func Statements(m Migration, table string) ([]Query, error) {
	b, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, err
	}
	var qs []Query
	for _, s := range Split(string(b)) {
		qs = append(qs, Query{SQL: s})
	}
	qs = append(qs, Query{SQL: "INSERT INTO " + table + " (name) VALUES (?)", Params: []any{m.Name}})
	return qs, nil
}

// Apply applies every pending migration in order, each as one batch, and
// stops at the first that fails (it is not recorded; nothing of it is
// applied). It returns the names it applied.
func Apply(ctx context.Context, exec Exec, dir, table string) ([]string, error) {
	pend, err := Pending(ctx, exec, dir, table)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range pend {
		qs, err := Statements(m, table)
		if err != nil {
			return done, err
		}
		if _, err := exec(ctx, qs); err != nil {
			return done, fmt.Errorf("migration %s: %w", m.Name, err)
		}
		done = append(done, m.Name)
	}
	return done, nil
}

var numRE = regexp.MustCompile(`^(\d+)_`)

// Create writes the next migration file in dir (wrangler's naming:
// 0001_<message>.sql, the message's spaces as underscores) and returns
// its path.
func Create(dir, message string, now time.Time) (string, error) {
	msg := strings.Join(strings.Fields(message), "_")
	if msg == "" || strings.ContainsAny(msg, `/\`) || strings.HasPrefix(msg, ".") {
		return "", fmt.Errorf("d1: bad migration message %q", message)
	}
	all, err := List(dir)
	if err != nil {
		return "", err
	}
	next := 1
	for _, m := range all {
		if s := numRE.FindStringSubmatch(m.Name); s != nil {
			if n, err := strconv.Atoi(s[1]); err == nil && n >= next {
				next = n + 1
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	num := fmt.Sprintf("%04d", next)
	p := filepath.Join(dir, num+"_"+msg+".sql")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "-- Migration number: %s \t %s\n", num, now.UTC().Format("2006-01-02T15:04:05.000Z"))
	return p, err
}

// Split splits SQL text into statements at top-level semicolons. It
// understands quoted strings and identifiers ('…', "…", `…`, […]),
// comments (-- and /* */), and trigger bodies (CREATE TRIGGER … BEGIN …
// END, where CASE … END nests). Statements that are only whitespace and
// comments are dropped; the others keep their comments.
func Split(sql string) []string {
	var out []string
	start := 0
	depth := 0         // BEGIN/CASE … END nesting inside a trigger body
	var words []string // the current statement's first keywords
	trigger := false   // the statement is a CREATE [TEMP] TRIGGER
	flush := func(end int) {
		s := strings.TrimSpace(sql[start:end])
		if s != "" && !onlyComments(s) {
			out = append(out, s)
		}
		start, depth, words, trigger = end+1, 0, nil, false
	}
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(sql, i, c, c)
		case c == '[':
			i = skipQuoted(sql, i, '[', ']')
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			if j := strings.Index(sql[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(sql)
			}
		case c == ';':
			if depth == 0 {
				flush(i)
			}
			i++
		case isWordStart(c):
			j := i
			for j < len(sql) && isWordChar(sql[j]) {
				j++
			}
			w := strings.ToUpper(sql[i:j])
			if len(words) < 4 {
				words = append(words, w)
				if len(words) >= 2 && words[0] == "CREATE" && (w == "TRIGGER") {
					trigger = true
				}
			}
			if trigger {
				switch w {
				case "BEGIN", "CASE":
					depth++
				case "END":
					if depth > 0 {
						depth--
					}
				}
			}
			i = j
		default:
			i++
		}
	}
	if start < len(sql) {
		flush(len(sql))
	}
	return out
}

func skipQuoted(s string, i int, open, close byte) int {
	i++ // past open
	for i < len(s) {
		if s[i] == close {
			if close != ']' && i+1 < len(s) && s[i+1] == close { // doubled quote
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
func isWordChar(c byte) bool { return isWordStart(c) || c >= '0' && c <= '9' || c == '$' }

func onlyComments(s string) bool {
	for i := 0; i < len(s); {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			i++
		case strings.HasPrefix(s[i:], "--"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				return true
			}
		default:
			return false
		}
	}
	return true
}
