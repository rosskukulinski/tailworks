package bundle

import (
	"strings"
	"testing"
)

func TestParsePython(t *testing.T) {
	files := func(wrangler, tw string) map[string][]byte {
		f := map[string][]byte{
			"wrangler.jsonc":                          []byte(wrangler),
			"tw-bundle.json":                          []byte(`{"main": "dist/entry.py", "modules": {"dist/python_modules/pkg/data.bin": "data"}}`),
			"dist/entry.py":                           []byte("x = 1\n"),
			"dist/helper.py":                          []byte("y = 2\n"),
			"dist/python_modules/pkg/__init__.py":     []byte(""),
			"dist/python_modules/pkg/data.bin":        []byte("d"),
			"dist/python_modules/pkg/notes.unshipped": []byte("not typed, so not a module"),
		}
		if tw != "" {
			f["tailworks.jsonc"] = []byte(tw)
		}
		return f
	}
	const ok = `{"name": "py", "main": "src/entry.py", "compatibility_date": "2026-04-01", "compatibility_flags": ["python_workers"],
  "durable_objects": {"bindings": [{"name": "C", "class_name": "Counter"}]}}`
	a, err := Parse(files(ok, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !a.Python || a.Main != "dist/entry.py" || a.PythonBase() != "dist/" {
		t.Fatalf("python %v main %q base %q", a.Python, a.Main, a.PythonBase())
	}
	if c := a.Classes["Counter"]; c.Node || c.Facet() {
		t.Fatalf("a Python DO class must be internal plain-DO: %+v", c)
	}
	for p, kind := range map[string]string{"dist/entry.py": "py", "dist/helper.py": "py", "dist/python_modules/pkg/__init__.py": "py", "dist/python_modules/pkg/data.bin": "data"} {
		if _, in := a.Modules[p]; !in || a.ModuleKind(p) != kind {
			t.Errorf("%s: in bundle %v, kind %q, want %q", p, in, a.ModuleKind(p), kind)
		}
	}
	if _, in := a.Modules["dist/python_modules/pkg/notes.unshipped"]; in {
		t.Error("an untyped file became a module")
	}
	if n := a.PythonModuleName("dist/python_modules/pkg/__init__.py"); n != "python_modules/pkg/__init__.py" {
		t.Errorf("module name %q", n)
	}

	for name, c := range map[string]struct{ wrangler, tw, want string }{
		"no flag":    {strings.Replace(ok, `"python_workers"`, `"nodejs_compat"`, 1), "", `add "python_workers"`},
		"containers": {strings.Replace(ok, `"compatibility_flags"`, `"containers": [{"class_name": "Counter", "image": "./Dockerfile"}], "compatibility_flags"`, 1), "", "containers: not supported for Python"},
		"workflow":   {strings.Replace(ok, `"compatibility_flags"`, `"workflows": [{"name": "w", "binding": "W", "class_name": "Flow"}], "compatibility_flags"`, 1), "", "workflows: not supported for Python"},
		"tags":       {ok, `{"tailnet": {"objects": {"Counter": {"tags": ["tag:x"]}}}}`, "no node of their own"},
	} {
		if _, err := Parse(files(c.wrangler, c.tw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
