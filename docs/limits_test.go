package docs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestLimitsDocCitations pins every code citation in limits.md (the format
// is described at the top of that page): each one must resolve to exactly
// one place in the cited file, and when the cited code is a constant
// expression its value must appear in the same table row. Changing a limit
// without updating limits.md fails here.
func TestLimitsDocCitations(t *testing.T) {
	b, err := os.ReadFile("limits.md")
	if err != nil {
		t.Fatal(err)
	}
	cites, checked := 0, 0
	for i, line := range strings.Split(string(b), "\n") {
		for _, m := range limitsLineNumRE.FindAllStringSubmatch(line, -1) {
			t.Errorf("limits.md:%d: `%s` cites a line number; cite a symbol, flag or #anchor instead (see the top of limits.md)", i+1, m[1])
		}
		var row []string // the row's cells, without citations
		if strings.HasPrefix(line, "|") {
			row = strings.Split(limitsCiteRE.ReplaceAllString(line, ""), "|")
		}
		for _, m := range limitsCiteRE.FindAllStringSubmatch(line, -1) {
			cites++
			vals, err := resolveLimitCite(m[1], m[2])
			if err != nil {
				t.Errorf("limits.md:%d: `%s:%s`: %v", i+1, m[1], m[2], err)
				continue
			}
			if len(vals) == 0 || row == nil {
				continue // nothing constant to compare
			}
			checked++
			if !rowHasValue(row, vals) {
				t.Errorf("limits.md:%d: `%s:%s` is %s in the code, which this row doesn't say; update the row (or the citation)",
					i+1, m[1], m[2], fmtVals(vals))
			}
		}
	}
	if cites < 100 {
		t.Errorf("found only %d citations in limits.md; is the citation pattern still right?", cites)
	}
	t.Logf("%d citations, %d with a value checked", cites, checked)
}

var (
	// `path/to/file.go:Target`
	limitsCiteRE = regexp.MustCompile("`([\\w./-]+\\.(?:go|js|ts)):([^`\\s]+)`")
	// `path/to/file.go:123`, the old, drifting form.
	limitsLineNumRE = regexp.MustCompile("`([\\w./-]+\\.(?:go|js|ts):\\d+)`")
)

// resolveLimitCite finds target in file and returns the values the cited
// code sets (none when it isn't a constant expression).
func resolveLimitCite(file, target string) ([]float64, error) {
	src, err := os.ReadFile(filepath.Join("..", file))
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(target, "#"):
		return resolveLimitAnchor(file, string(src), target[1:])
	case strings.HasSuffix(file, ".go"):
		return resolveGoSymbol(file, src, target)
	default:
		return resolveJSSymbol(string(src), target)
	}
}

// resolveLimitAnchor finds the one "limits.md#slug" comment in src. The
// cited line is the comment's own line, or the next line of code when the
// comment stands alone. Every numeric constant expression on that line is
// a candidate value (zeros are skipped: they are nearly always a "> 0"
// test, not the limit).
func resolveLimitAnchor(file, src, slug string) ([]float64, error) {
	re := regexp.MustCompile(`limits\.md#` + regexp.QuoteMeta(slug) + `(?:[^\w-]|$)`)
	lines := strings.Split(src, "\n")
	at := -1
	for i, l := range lines {
		if re.MatchString(l) {
			if at >= 0 {
				return nil, fmt.Errorf("anchor limits.md#%s is in %s twice (lines %d and %d)", slug, file, at+1, i+1)
			}
			at = i
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("no // limits.md#%s comment in %s", slug, file)
	}
	code := stripLineComment(lines[at])
	for strings.TrimSpace(code) == "" || strings.HasPrefix(strings.TrimSpace(code), "*") {
		if at++; at >= len(lines) {
			return nil, fmt.Errorf("anchor limits.md#%s is not followed by code", slug)
		}
		code = stripLineComment(lines[at])
	}
	consts := func(name string) (float64, bool) { return 0, false }
	var vals []float64
	for _, m := range numExprRE.FindAllStringSubmatch(code, -1) {
		e, err := parser.ParseExpr(m[1])
		if err != nil {
			continue
		}
		if v, ok := evalLimitExpr(e, consts, 0); ok && v != 0 {
			vals = append(vals, v)
		}
	}
	return vals, nil
}

func stripLineComment(l string) string {
	if i := strings.Index(l, "//"); i >= 0 {
		return l[:i]
	}
	return l
}

// numExprRE matches arithmetic on numbers and time units: 2*time.Minute,
// 64<<20, 5 * 1024 * 1024.
var numExprRE = regexp.MustCompile(`(?:^|[^\w.])((?:\d[\d_]*(?:\.\d+)?|time\.[A-Z]\w*)(?:\s*(?:\*|<<|/|\+|-)\s*(?:\d[\d_]*(?:\.\d+)?|time\.[A-Z]\w*))*)`)

// resolveGoSymbol resolves Name, Name.Key.Key or -flag in a Go file.
//
//   - Name is a const or var declaration (at any scope) if there is one;
//     otherwise an assignment to Name or x.Name, a Name: value field in a
//     composite literal, or a def(&x.Name, value) call. It must be unique.
//   - Name.Key... descends into Name's composite literal by field name or
//     map key.
//   - -flag is a flag definition: any call x.Kind("flag", default, ...).
func resolveGoSymbol(file string, src []byte, target string) ([]float64, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var decls, uses []ast.Expr // value expressions (nil: declared without one)
	var hit func(ast.Expr, string) bool
	hit = func(e ast.Expr, name string) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return e.Name == name
		case *ast.SelectorExpr:
			return e.Sel.Name == name
		case *ast.UnaryExpr:
			return e.Op == token.AND && hit(e.X, name)
		}
		return false
	}
	if flag, ok := strings.CutPrefix(target, "-"); ok {
		quoted := strconv.Quote(flag)
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && len(c.Args) >= 2 {
				if _, sel := c.Fun.(*ast.SelectorExpr); sel {
					if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Value == quoted {
						decls = append(decls, c.Args[1])
					}
				}
			}
			return true
		})
		return goValues(f, target, decls)
	}
	path := strings.Split(target, ".")
	name := path[0]
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name == name {
					var v ast.Expr
					if i < len(n.Values) {
						v = n.Values[i]
					}
					decls = append(decls, v)
				}
			}
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, l := range n.Lhs {
					if hit(l, name) {
						uses = append(uses, n.Rhs[i])
					}
				}
			}
		case *ast.KeyValueExpr:
			if hit(n.Key, name) {
				uses = append(uses, n.Value)
			}
		case *ast.CallExpr:
			if len(n.Args) == 2 {
				if u, ok := n.Args[0].(*ast.UnaryExpr); ok && hit(u, name) {
					uses = append(uses, n.Args[1])
				}
			}
		}
		return true
	})
	if len(decls) == 0 {
		decls = uses
	}
	if len(decls) == 1 {
		e := decls[0]
		for _, key := range path[1:] {
			if e = compositeField(e, key); e == nil {
				return nil, fmt.Errorf("%s has no field or key %q", name, key)
			}
		}
		decls[0] = e
	}
	return goValues(f, target, decls)
}

func goValues(f *ast.File, target string, found []ast.Expr) ([]float64, error) {
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s not found (renamed or removed?)", target)
	case 1:
	default:
		return nil, fmt.Errorf("%s matches %d places; cite a unique symbol or add a // limits.md#slug anchor", target, len(found))
	}
	if found[0] == nil {
		return nil, nil
	}
	consts := func(name string) (float64, bool) {
		var e ast.Expr
		n := 0
		ast.Inspect(f, func(x ast.Node) bool {
			if s, ok := x.(*ast.ValueSpec); ok {
				for i, id := range s.Names {
					if id.Name == name && i < len(s.Values) {
						e, n = s.Values[i], n+1
					}
				}
			}
			return true
		})
		if n != 1 {
			return 0, false
		}
		return evalLimitExpr(e, nil, 1)
	}
	if v, ok := evalLimitExpr(found[0], consts, 0); ok {
		return []float64{v}, nil
	}
	return nil, nil
}

// compositeField returns the value of key in composite literal e.
func compositeField(e ast.Expr, key string) ast.Expr {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		switch k := kv.Key.(type) {
		case *ast.Ident:
			if k.Name == key {
				return kv.Value
			}
		case *ast.BasicLit:
			if s, err := strconv.Unquote(k.Value); err == nil && s == key {
				return kv.Value
			}
		}
	}
	return nil
}

// resolveJSSymbol resolves NAME or OBJ.key in a JS or TS file: the one line
// "[export] const NAME = value" or "NAME: value"; for OBJ.key, the one
// "key: value" line in the object literal that OBJ's line opens.
func resolveJSSymbol(src, target string) ([]float64, error) {
	path := strings.Split(target, ".")
	if len(path) > 2 {
		return nil, fmt.Errorf("%s: JS citations are NAME or OBJ.key", target)
	}
	declIn := func(src, name string) []string {
		re := regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:(?:const|let|var)\s+)?` + regexp.QuoteMeta(name) + `\s*[:=]\s*([^=>].*?)\s*[,;]?\s*(?://.*)?$`)
		var vals []string
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			vals = append(vals, m[1])
		}
		return vals
	}
	decl := func(name string) []string { return declIn(src, name) }
	scope := src
	if len(path) == 2 {
		re := regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+` + regexp.QuoteMeta(path[0]) + `\s*=.*\{\s*$`)
		locs := re.FindAllStringIndex(src, -1)
		if len(locs) != 1 {
			return nil, fmt.Errorf("%s: want one object literal declared as const %s = ...{, found %d", target, path[0], len(locs))
		}
		scope = src[locs[0][1]:]
		if end := regexp.MustCompile(`(?m)^\s*\}`).FindStringIndex(scope); end != nil {
			scope = scope[:end[0]]
		}
	}
	found := declIn(scope, path[len(path)-1])
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s not found (renamed or removed?)", target)
	case 1:
	default:
		return nil, fmt.Errorf("%s matches %d places; cite a unique name or add a // limits.md#slug anchor", target, len(found))
	}
	consts := func(name string) (float64, bool) {
		d := decl(name)
		if len(d) != 1 {
			return 0, false
		}
		e, err := parser.ParseExpr(d[0])
		if err != nil {
			return 0, false
		}
		return evalLimitExpr(e, nil, 1)
	}
	e, err := parser.ParseExpr(found[0])
	if err != nil {
		return nil, nil
	}
	if v, ok := evalLimitExpr(e, consts, 0); ok {
		return []float64{v}, nil
	}
	return nil, nil
}

var timeUnits = map[string]float64{
	"Nanosecond":  1,
	"Microsecond": 1e3,
	"Millisecond": 1e6,
	"Second":      1e9,
	"Minute":      60e9,
	"Hour":        3600e9,
}

// evalLimitExpr evaluates a constant expression: numbers, + - * / << >>,
// time units, conversions such as time.Duration(x), and (through consts)
// other constants in the same file. depth bounds that indirection.
func evalLimitExpr(e ast.Expr, consts func(string) (float64, bool), depth int) (float64, bool) {
	rec := func(e ast.Expr) (float64, bool) { return evalLimitExpr(e, consts, depth) }
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT && e.Kind != token.FLOAT {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(e.Value, "_", ""), 64)
		return v, err == nil
	case *ast.ParenExpr:
		return rec(e.X)
	case *ast.UnaryExpr:
		v, ok := rec(e.X)
		if e.Op == token.SUB {
			v = -v
		}
		return v, ok && (e.Op == token.SUB || e.Op == token.ADD)
	case *ast.BinaryExpr:
		x, ok1 := rec(e.X)
		y, ok2 := rec(e.Y)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch e.Op {
		case token.ADD:
			return x + y, true
		case token.SUB:
			return x - y, true
		case token.MUL:
			return x * y, true
		case token.QUO:
			return x / y, y != 0
		case token.SHL:
			return x * math.Pow(2, y), true
		case token.SHR:
			return math.Floor(x / math.Pow(2, y)), true
		}
	case *ast.SelectorExpr:
		if p, ok := e.X.(*ast.Ident); ok && p.Name == "time" {
			v, ok := timeUnits[e.Sel.Name]
			return v, ok
		}
	case *ast.Ident:
		if consts != nil && depth < 4 {
			return consts(e.Name)
		}
	case *ast.CallExpr: // a conversion: time.Duration(x), int64(x)
		if len(e.Args) == 1 {
			switch f := e.Fun.(type) {
			case *ast.Ident:
				return rec(e.Args[0])
			case *ast.SelectorExpr:
				if p, ok := f.X.(*ast.Ident); ok && p.Name == "time" && f.Sel.Name == "Duration" {
					return rec(e.Args[0])
				}
			}
		}
	}
	return 0, false
}

// docQtyRE matches a quantity in a limits.md row: "64 MiB", "43,200 s",
// "75%", "1/16", optionally as a difference ("2 MiB − 4 KiB").
var docQtyRE = regexp.MustCompile(`(\d{1,3}(?:,\d{3})+|\d+(?:\.\d+)?)(?:/(\d+))?(?:\s*(%|(?:KiB|MiB|GiB|Mi|KB|MB|GB|bytes|byte|B|ms|min|minutes|minute|seconds|second|s|hours|hour|h)\b))?`)

var unitScale = map[string]struct {
	base  float64
	scale []float64 // the code may hold the base value divided by any of these
}{
	"":        {1, []float64{1}},
	"%":       {0.01, []float64{1, 0.01}},
	"B":       {1, []float64{1}},
	"byte":    {1, []float64{1}},
	"bytes":   {1, []float64{1}},
	"KiB":     {1 << 10, []float64{1, 1 << 10}},
	"MiB":     {1 << 20, []float64{1, 1 << 10, 1 << 20}},
	"GiB":     {1 << 30, []float64{1, 1 << 10, 1 << 20}},
	"Mi":      {1 << 20, []float64{1}},
	"KB":      {1e3, []float64{1, 1e3}},
	"MB":      {1e6, []float64{1, 1e3, 1e6}},
	"GB":      {1e9, []float64{1, 1e3, 1e6}},
	"ms":      {1e-3, secScales},
	"s":       {1, secScales},
	"second":  {1, secScales},
	"seconds": {1, secScales},
	"min":     {60, secScales},
	"minute":  {60, secScales},
	"minutes": {60, secScales},
	"h":       {3600, secScales},
	"hour":    {3600, secScales},
	"hours":   {3600, secScales},
}

// A time in seconds may be held in seconds, milliseconds or a
// time.Duration (nanoseconds).
var secScales = []float64{1, 1e-3, 1e-9}

// rowHasValue reports whether a quantity in row equals one of vals (in any
// unit the code might use). "unlimited", "none", "auto" and "off" stand for
// 0.
func rowHasValue(row []string, vals []float64) bool {
	text := strings.Join(row, " | ")
	var cands []float64
	add := func(base float64, unit string) {
		u := unitScale[unit]
		for _, s := range u.scale {
			cands = append(cands, base/s)
		}
		cands = append(cands, base/u.base) // the number as written
	}
	type qty struct {
		base       float64
		unit       string
		start, end int
	}
	var qs []qty
	for _, m := range docQtyRE.FindAllStringSubmatchIndex(text, -1) {
		n, _ := strconv.ParseFloat(strings.ReplaceAll(text[m[2]:m[3]], ",", ""), 64)
		if m[4] >= 0 { // a fraction
			d, _ := strconv.ParseFloat(text[m[4]:m[5]], 64)
			cands = append(cands, n/d)
		}
		unit := ""
		if m[6] >= 0 {
			unit = text[m[6]:m[7]]
		}
		q := qty{n * unitScale[unit].base, unit, m[0], m[1]}
		add(q.base, q.unit)
		if len(qs) > 0 {
			p := qs[len(qs)-1]
			if between := strings.TrimSpace(text[p.end:q.start]); (between == "−" || between == "-") && p.unit != "" && q.unit != "" {
				add(p.base-q.base, p.unit)
			}
		}
		qs = append(qs, q)
	}
	if regexp.MustCompile(`(?i)\b(unlimited|none|auto|off)\b`).MatchString(text) {
		cands = append(cands, 0)
	}
	for _, v := range vals {
		for _, c := range cands {
			if math.Abs(v-c) <= 1e-9*math.Max(1, math.Abs(v)) {
				return true
			}
		}
	}
	return false
}

func fmtVals(vals []float64) string {
	s := make([]string, len(vals))
	for i, v := range vals {
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			s[i] = strconv.FormatFloat(v, 'f', -1, 64)
		} else {
			s[i] = strconv.FormatFloat(v, 'g', -1, 64)
		}
	}
	return strings.Join(s, " or ")
}
