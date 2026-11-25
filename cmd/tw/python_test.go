package main

import "testing"

func TestIsPythonApp(t *testing.T) {
	for _, c := range []struct {
		w    map[string]any
		want bool
	}{
		{map[string]any{"main": "src/entry.py"}, true},
		{map[string]any{"main": "src/index.ts", "compatibility_flags": []any{"python_workers"}}, true},
		{map[string]any{"main": "src/index.ts", "compatibility_flags": []any{"nodejs_compat"}}, false},
		{map[string]any{}, false},
	} {
		if got := isPythonApp(c.w); got != c.want {
			t.Errorf("%v: %v, want %v", c.w, got, c.want)
		}
	}
}

func TestImportsWorkersSDK(t *testing.T) {
	for src, want := range map[string]bool{
		"from workers import WorkerEntrypoint\n": true,
		"import workers\n":                       true,
		"  from workers.foo import x\n":          true,
		"from workers_extra import x\n":          false,
		"import json\n":                          false,
	} {
		if got := importsWorkersSDK([][]byte{[]byte(src)}); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

func TestDeclaresPackages(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       bool
	}{
		{"pyproject.toml", "[project]\nname = \"x\"\ndependencies = []\n\n[dependency-groups]\ndev = [\"workers-py\"]\n", false},
		{"pyproject.toml", "[project]\ndependencies = [\n  \"fastapi\",\n]\n", true},
		{"requirements.txt", "# none\n\n", false},
		{"requirements.txt", "fastapi\n", true},
	} {
		if got := declaresPackages(c.name, []byte(c.body)); got != c.want {
			t.Errorf("%s %q: %v, want %v", c.name, c.body, got, c.want)
		}
	}
}
