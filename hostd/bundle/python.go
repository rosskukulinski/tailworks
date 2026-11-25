package bundle

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Python Workers (Pyodide; ADR-055, docs/python.md).
//
// A bundle whose main module is a .py file is a Python Worker. hostd runs
// its modules in a Python worker of their own behind a generated
// JavaScript front (hostd/gen/python.go), so the front keeps the caller
// seal, the public/tailnet split and tracing. Python Durable Objects are
// internal objects: plain-DO mode with no node of their own.

// PythonFlag is the compatibility flag every Python Worker sets.
const PythonFlag = "python_workers"

// PythonModulesDir is where vendored packages live, next to the main
// module (pywrangler's python_modules/).
const PythonModulesDir = "python_modules/"

// IsPythonMain reports whether main names a Python module.
func IsPythonMain(main string) bool { return strings.EqualFold(path.Ext(main), ".py") }

// PythonBase is the directory Python module names are relative to: the
// main module's directory ("" at the bundle root, else "dir/").
func (a *App) PythonBase() string {
	d := path.Dir(a.Main)
	if d == "." {
		return ""
	}
	return d + "/"
}

// PythonModuleName is p's module name in the Python worker (p relative to
// PythonBase), or "" if p is outside it.
func (a *App) PythonModuleName(p string) string {
	b := a.PythonBase()
	if !strings.HasPrefix(p, b) {
		return ""
	}
	return strings.TrimPrefix(p, b)
}

// finishPython marks a Python app and checks what it uses. Called at the
// end of Parse, when every other part of the config is known.
func finishPython(a *App) error {
	if !IsPythonMain(a.Main) {
		return nil
	}
	a.Python = true
	if !slices.Contains(a.CompatFlags, PythonFlag) {
		return fmt.Errorf("bundle: main %s is a Python module: add %q to compatibility_flags (as on Cloudflare)", a.Main, PythonFlag)
	}
	for p := range a.Modules {
		if a.PythonModuleName(p) == "" {
			return fmt.Errorf("bundle: module %s is outside the Python main module's directory %s", p, a.PythonBase())
		}
	}
	unsupported := func(what string) error {
		return fmt.Errorf("bundle: %s: not supported for Python Workers yet (docs/python.md#limits)", what)
	}
	switch {
	case len(a.Workflows) > 0:
		return unsupported("workflows")
	case len(a.Containers) > 0:
		return unsupported("containers")
	}
	// Python Durable Objects are internal: plain-DO mode, no node of their
	// own (the per-object wrapper is JavaScript; hostd/gen/python.go).
	for _, n := range a.ClassNames() {
		c := a.Classes[n]
		if len(c.Tags) > 0 || c.Public != nil || c.TTL != "" {
			return fmt.Errorf("bundle: tailworks.jsonc tailnet.objects.%s: Python Durable Objects have no node of their own, so no tags, ttl or public (docs/python.md#durable-objects)", n)
		}
		c.Node, c.Egress = false, EgressWorkload
	}
	return nil
}
