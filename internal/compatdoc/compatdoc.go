// Package compatdoc keeps the README's "Compatibility at a glance" table and
// docs/COMPAT.md's surface rows in step, and checks that every row claiming
// something works names a test that exists ("measured, not claimed").
//
// The format (documented at the top of docs/COMPAT.md):
//
//   - a COMPAT.md surface row is a Markdown table row whose first cell starts
//     with an anchor, `<a id="<id>"></a>`. Its second cell is the status and
//     its last cell lists the tests, each as `<path>:<TestName>[/<subtest>…]`
//     in backticks;
//   - a README row (in the "## Compatibility at a glance" section) links its
//     surface to `docs/COMPAT.md#<id>` and has the same status in the cell
//     after it.
//
// A test reference is resolved without running anything: <path> is a Go
// package directory (TestName must be a `func TestName(` in one of its
// _test.go files, any build tag; each subtest must appear as a string
// literal, or the start of one, in those files) or a test file (each name
// must appear as a string literal in it).
package compatdoc

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The statuses a row may have.
const (
	Works    = "✅"
	Partial  = "⚠️"
	Missing  = "❌"
	ByDesign = "🚫"
)

// ReadmeSection is the README heading the table lives under.
const ReadmeSection = "## Compatibility at a glance"

// Row is one surface row.
type Row struct {
	ID     string
	Status string
	Tests  []string // COMPAT.md rows only
	Line   int

	// TestsCell is the raw last cell (COMPAT.md rows only).
	TestsCell string
}

var (
	anchorRE  = regexp.MustCompile(`^<a id="([a-z0-9][a-z0-9-]*)"></a>`)
	linkRE    = regexp.MustCompile(`\]\((?:\./)?docs/COMPAT\.md#([a-z0-9][a-z0-9-]*)\)`)
	backtick  = regexp.MustCompile("`([^`]+)`")
	testRefRE = regexp.MustCompile(`^([A-Za-z0-9_./-]+):(\S.*)$`)
)

// cells splits a Markdown table row ("| a | b |") into trimmed cells. It
// returns nil for lines that are not table rows. `\|` inside a cell is kept.
func cells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") || len(line) < 2 {
		return nil
	}
	var out []string
	var cur strings.Builder
	inner := line[1 : len(line)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) && inner[i+1] == '|' {
			cur.WriteString(`\|`)
			i++
			continue
		}
		if inner[i] == '|' {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(inner[i])
	}
	return append(out, strings.TrimSpace(cur.String()))
}

// status returns the status emoji a cell starts with ("" if none).
func status(cell string) string {
	for _, s := range []string{Works, Partial, Missing, ByDesign} {
		if strings.HasPrefix(cell, s) {
			return s
		}
	}
	// ⚠ without the variation selector.
	if strings.HasPrefix(cell, "⚠") {
		return Partial
	}
	return ""
}

// ParseCompat returns COMPAT.md's surface rows.
func ParseCompat(md []byte) ([]Row, []error) {
	var rows []Row
	var errs []error
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		c := cells(sc.Text())
		if len(c) == 0 {
			continue
		}
		m := anchorRE.FindStringSubmatch(c[0])
		if m == nil {
			continue
		}
		if len(c) < 3 {
			errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface row %q needs a status and a tests cell", n, m[1]))
			continue
		}
		r := Row{ID: m[1], Status: status(c[1]), Line: n, TestsCell: c[len(c)-1]}
		for _, b := range backtick.FindAllStringSubmatch(c[len(c)-1], -1) {
			if testRefRE.MatchString(b[1]) {
				r.Tests = append(r.Tests, b[1])
			}
		}
		rows = append(rows, r)
	}
	return rows, errs
}

// ParseReadme returns the rows of the README's compatibility table.
func ParseReadme(md []byte) ([]Row, []error) {
	var rows []Row
	var errs []error
	in, found := false, false
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(line) == ReadmeSection
			found = found || in
			continue
		}
		if !in {
			continue
		}
		c := cells(line)
		if len(c) == 0 || strings.Trim(strings.Join(c, ""), "-: ") == "" {
			continue // not a row, or the |---| separator
		}
		for i, cell := range c {
			m := linkRE.FindStringSubmatch(cell)
			if m == nil {
				continue
			}
			r := Row{ID: m[1], Line: n}
			if i+1 < len(c) {
				r.Status = status(c[i+1])
			}
			rows = append(rows, r)
			break
		}
	}
	if !found {
		errs = append(errs, fmt.Errorf("README.md: no %q section", ReadmeSection))
	} else if len(rows) == 0 {
		errs = append(errs, fmt.Errorf("README.md: %q has no rows linking docs/COMPAT.md#<id>", ReadmeSection))
	}
	return rows, errs
}

// Check checks README.md and docs/COMPAT.md under root.
func Check(root string) []error {
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return []error{err}
	}
	compat, err := os.ReadFile(filepath.Join(root, "docs", "COMPAT.md"))
	if err != nil {
		return []error{err}
	}
	return CheckDocs(root, readme, compat)
}

// CheckDocs checks the two documents; test references resolve under root.
func CheckDocs(root string, readme, compat []byte) []error {
	crows, errs := ParseCompat(compat)
	rrows, rerrs := ParseReadme(readme)
	errs = append(errs, rerrs...)
	byID := map[string]Row{}
	for _, r := range crows {
		if prev, dup := byID[r.ID]; dup {
			errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface %q is already defined on line %d", r.Line, r.ID, prev.Line))
			continue
		}
		byID[r.ID] = r
		switch r.Status {
		case "":
			errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface %q: status must start with one of %s %s %s %s", r.Line, r.ID, Works, Partial, Missing, ByDesign))
		case Works, Partial:
			if len(r.Tests) == 0 {
				errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface %q is %s but names no test (`<path>:<TestName>` in the last cell)", r.Line, r.ID, r.Status))
			}
		}
		for _, ref := range r.Tests {
			if err := resolveTest(root, ref); err != nil {
				errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface %q: test `%s`: %v", r.Line, r.ID, ref, err))
			}
		}
		if err := checkNightly(root, r); err != nil {
			errs = append(errs, err)
		}
	}
	inReadme := map[string]bool{}
	for _, r := range rrows {
		if inReadme[r.ID] {
			errs = append(errs, fmt.Errorf("README.md:%d: %q is listed twice", r.Line, r.ID))
		}
		inReadme[r.ID] = true
		c, ok := byID[r.ID]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("README.md:%d: docs/COMPAT.md#%s has no surface row (`<a id=%q></a>` in a table row)", r.Line, r.ID, r.ID))
		case r.Status == "":
			errs = append(errs, fmt.Errorf("README.md:%d: %q: the cell after the link must be its status", r.Line, r.ID))
		case r.Status != c.Status:
			errs = append(errs, fmt.Errorf("README.md:%d: %q is %s here but %s in docs/COMPAT.md:%d", r.Line, r.ID, r.Status, c.Status, c.Line))
		}
	}
	var missing []string
	for id := range byID {
		if !inReadme[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		errs = append(errs, fmt.Errorf("docs/COMPAT.md:%d: surface %q has no row in README.md %q", byID[id].Line, id, ReadmeSection))
	}
	return errs
}

// resolveTest checks that ref (`<path>:<Name>[/<sub>…]`) names a test that
// exists under root.
func resolveTest(root, ref string) error {
	m := testRefRE.FindStringSubmatch(ref)
	if m == nil {
		return fmt.Errorf("want <path>:<TestName>")
	}
	p := filepath.Join(root, filepath.FromSlash(m[1]))
	names := strings.Split(m[2], "/")
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%s does not exist", m[1])
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, n := range names {
			if !hasLiteral(b, n) {
				return fmt.Errorf("%q is not in %s", n, m[1])
			}
		}
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(p, "*_test.go"))
	if len(files) == 0 {
		return fmt.Errorf("%s has no _test.go files", m[1])
	}
	var all []byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		all = append(all, b...)
		all = append(all, '\n')
	}
	if !regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(names[0]) + `\(t \*testing\.T\)`).Match(all) {
		return fmt.Errorf("no func %s(t *testing.T) in %s/*_test.go", names[0], m[1])
	}
	for _, sub := range names[1:] {
		if !hasLiteral(all, sub) {
			return fmt.Errorf("subtest %q of %s is not in %s/*_test.go", sub, names[0], m[1])
		}
	}
	return nil
}

// hasLiteral reports whether src has a string literal that is s, or starts
// with s followed by "/" (a subtest name built at run time: "Prefix/"+k).
func hasLiteral(src []byte, s string) bool {
	for _, q := range []string{`"`, "`", `'`} {
		if bytes.Contains(src, []byte(q+s+q)) || bytes.Contains(src, []byte(q+s+"/")) {
			return true
		}
	}
	return false
}
