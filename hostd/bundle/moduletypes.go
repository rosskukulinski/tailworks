package bundle

import (
	"fmt"
	"path"
	"strings"
)

// setModuleTypes records tw-bundle.json "modules" (path -> type). Every
// listed path must be in the bundle and have a type workerd knows.
func (a *App) setModuleTypes(files map[string][]byte, types map[string]string) error {
	for p, t := range types {
		switch t {
		case "js",
			"json",
			"text",
			"data",
			"wasm",
			"py": // a Python module (python.go)
		default:
			return fmt.Errorf("bundle: tw-bundle.json modules[%q]: type %q: want js, json, text, data, wasm or py", p, t)
		}
		if _, ok := files[p]; !ok {
			return fmt.Errorf("bundle: tw-bundle.json modules[%q]: not in bundle", p)
		}
		if a.ModuleTypes == nil {
			a.ModuleTypes = map[string]string{}
		}
		a.ModuleTypes[p] = t
	}
	return nil
}

// ModuleKind is p's module type: the bundler's explicit type, else by
// extension (.js/.mjs js, .json json, .txt/.html text, .wasm wasm, .py
// py, else data).
func (a *App) ModuleKind(p string) string {
	if t, ok := a.ModuleTypes[p]; ok {
		return t
	}
	return KindByExt(p)
}

// KindByExt types a module by its extension.
func KindByExt(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".mjs":
		return "js"
	case ".json":
		return "json"
	case ".txt", ".html":
		return "text"
	case ".wasm":
		return "wasm"
	case ".py":
		return "py"
	}
	return "data"
}
