package srcmap

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// build bundles files (path -> TypeScript) with esbuild as tailworks does and
// returns the module and its map.
func build(t *testing.T, files map[string]string, entry string) (js string, m *Map) {
	t.Helper()
	dir := t.TempDir()
	for p, s := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := api.Build(api.BuildOptions{
		EntryPoints: []string{filepath.Join(dir, entry)}, Outfile: filepath.Join(dir, "dist", "index.js"), AbsWorkingDir: dir,
		Bundle: true, Format: api.FormatESModule, Platform: api.PlatformNeutral, Sourcemap: api.SourceMapExternal, LogLevel: api.LogLevelSilent,
	})
	if len(r.Errors) > 0 {
		t.Fatalf("esbuild: %v", r.Errors)
	}
	var raw []byte
	for _, f := range r.OutputFiles {
		switch {
		case strings.HasSuffix(f.Path, ".js.map"):
			raw = f.Contents
		case strings.HasSuffix(f.Path, ".js"):
			js = string(f.Contents)
		}
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return js, m
}

// posOf is the 1-based line and column of the first sub in s.
func posOf(t *testing.T, s, sub string) (int, int) {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q not in %q", sub, s)
	}
	line := strings.Count(s[:i], "\n") + 1
	col := i - strings.LastIndex(s[:i], "\n")
	return line, col
}

func TestLookupEsbuild(t *testing.T) {
	boom := "// a helper\n\nexport function explode(where: string): never {\n  const n: number = 1;\n  throw new Error(\"boom at \" + where + n);\n}\n"
	index := "import { explode } from \"./boom\";\n\nexport default {\n  async fetch(req: Request): Promise<Response> {\n    if (req.url.endsWith(\"/boom\")) explode(\"x\");\n    return new Response(\"ok\");\n  },\n};\n"
	js, m := build(t, map[string]string{"src/boom.ts": boom, "src/index.ts": index}, "src/index.ts")

	// The throw in the bundle maps to line 5 of src/boom.ts.
	l, c := posOf(t, js, "throw new Error")
	p, ok := m.Lookup(l, c)
	if !ok || !strings.HasSuffix(p.Source, "src/boom.ts") || p.Line != 5 || p.Column != 3 {
		t.Fatalf("throw at %d:%d -> %+v %v", l, c, p, ok)
	}
	// The call site in fetch maps to line 5 of src/index.ts.
	l, c = posOf(t, js, "explode(\"x\")")
	p, ok = m.Lookup(l, c)
	if !ok || !strings.HasSuffix(p.Source, "src/index.ts") || p.Line != 5 {
		t.Fatalf("call at %d:%d -> %+v %v", l, c, p, ok)
	}

	// A V8 stack from that bundle, as workerd reports it.
	tl, tc := posOf(t, js, "throw new Error")
	cl, cc := posOf(t, js, "explode(\"x\")")
	stack := "Error: boom at x1\n" +
		"    at explode (dist/index.js:" + strconv.Itoa(tl) + ":" + strconv.Itoa(tc) + ")\n" +
		"    at Object.fetch (dist/index.js:" + strconv.Itoa(cl) + ":" + strconv.Itoa(cc) + ")\n" +
		"    at __tw_rt.js:120:15\n" +
		"    at async Promise.all (index 0)"
	got := MapStack(stack, func(mod string) *Map {
		if mod == "dist/index.js" {
			return m
		}
		return nil
	})
	for _, want := range []string{"at explode (", "src/boom.ts:5:3)", "at Object.fetch (", "src/index.ts:5:", "at __tw_rt.js:120:15", "at async Promise.all (index 0)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("mapped stack lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dist/index.js") {
		t.Fatalf("a frame was left unmapped:\n%s", got)
	}
}

func TestLookupHandMade(t *testing.T) {
	// Line 1: AAAA = [0,0,0,0] (col 0 -> a.ts 1:1), UCEIA = [+10,+1,+2,+4,+0]
	// (col 10 -> b.ts 3:5, name "f"). Line 2: A = [0] (col 0 unmapped),
	// IAEJ = [+4,+0,+2,-4] (col 4 -> b.ts 5:1; source state carries over
	// from line 1, the generated column restarts).
	m, err := Parse([]byte(`{"version":3,"sources":["a.ts","b.ts"],"names":["f"],"mappings":"AAAA,UCEIA;A,IAEJ"}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		line, col int
		want      Pos
		ok        bool
	}{
		{1, 1, Pos{"a.ts", 1, 1, ""}, true},
		{1, 10, Pos{"a.ts", 1, 1, ""}, true},
		{1, 11, Pos{"b.ts", 3, 5, "f"}, true},
		{1, 99, Pos{"b.ts", 3, 5, "f"}, true},
		{2, 1, Pos{}, false}, // an unmapped segment
		{2, 5, Pos{"b.ts", 5, 1, ""}, true},
		{3, 1, Pos{}, false}, // no such line
		{0, 1, Pos{}, false},
	}
	for _, c := range cases {
		p, ok := m.Lookup(c.line, c.col)
		if ok != c.ok || ok && p != c.want {
			t.Errorf("Lookup(%d,%d) = %+v %v, want %+v %v", c.line, c.col, p, ok, c.want, c.ok)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":       `{`,
		"version 2":      `{"version":2,"sources":[],"mappings":""}`,
		"index map":      `{"version":3,"sections":[]}`,
		"bad char":       `{"version":3,"sources":["a"],"mappings":"A!AA"}`,
		"source range":   `{"version":3,"sources":["a"],"mappings":"ACAA"}`,
		"name range":     `{"version":3,"sources":["a"],"names":[],"mappings":"AAAAA"}`,
		"two fields":     `{"version":3,"sources":["a"],"mappings":"AA"}`,
		"truncated VLQ":  `{"version":3,"sources":["a"],"mappings":"AAAg"}`,
		"six fields":     `{"version":3,"sources":["a"],"names":["x"],"mappings":"AAAAAA"}`,
		"negative line":  `{"version":3,"sources":["a"],"mappings":"AADA"}`,
		"overlong VLQ":   `{"version":3,"sources":["a"],"mappings":"gggggggggA"}`,
		"too big (size)": `{"version":3,"sources":["a"],"mappings":"` + strings.Repeat("A", MaxMapBytes) + `"}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestMapStackLeavesUnknown(t *testing.T) {
	s := "Error: x\n    at f (dist/other.js:1:1)\n    at g (worker:1:2)"
	if got := MapStack(s, func(string) *Map { return nil }); got != s {
		t.Fatalf("changed: %q", got)
	}
	if got := MapStack(s, nil); got != s {
		t.Fatalf("changed: %q", got)
	}
}

func TestModuleName(t *testing.T) {
	for in, want := range map[string]string{
		"dist/index.js":          "dist/index.js",
		"./dist/index.js":        "dist/index.js",
		"/dist/index.js":         "dist/index.js",
		"file:///dist/index.js":  "dist/index.js",
		"worker://dist/index.js": "dist/index.js",
	} {
		if got := ModuleName(in); got != want {
			t.Errorf("ModuleName(%q) = %q, want %q", in, got, want)
		}
	}
}
