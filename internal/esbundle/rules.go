package esbundle

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

// Rule is one wrangler "rules" entry: imports whose specifier matches one
// of Globs are not bundled but shipped as an additional module of Type,
// next to the entry, as wrangler does.
type Rule struct {
	Type        string   `json:"type"` // "Text" | "Data" | "CompiledWasm"
	Globs       []string `json:"globs"`
	Fallthrough bool     `json:"fallthrough"`
}

// DefaultRules are wrangler's default module rules (wrangler 4).
var DefaultRules = []Rule{
	{Type: "Text", Globs: []string{"**/*.txt", "**/*.html", "**/*.sql"}},
	{Type: "Data", Globs: []string{"**/*.bin"}},
	{Type: "CompiledWasm", Globs: []string{"**/*.wasm", "**/*.wasm?module"}},
}

// ModuleType maps a wrangler rule type to the tailworks bundle module type
// (tw-bundle.json "modules"): "text", "data", "wasm" or "js"; "" for a
// type tailworks can't ship (CommonJS, Python).
func ModuleType(ruleType string) string {
	switch ruleType {
	case "Text":
		return "text"
	case "Data":
		return "data"
	case "CompiledWasm":
		return "wasm"
	case "ESModule":
		return "js"
	}
	return ""
}

// EffectiveRules is wrangler's rule order: the config's rules first, then
// each default rule unless a config rule of the same type without
// "fallthrough": true replaces it.
func EffectiveRules(user []Rule) []Rule {
	out := append([]Rule{}, user...)
	replaced := map[string]bool{}
	for _, r := range user {
		if !r.Fallthrough {
			replaced[r.Type] = true
		}
	}
	for _, r := range DefaultRules {
		if !replaced[r.Type] {
			out = append(out, r)
		}
	}
	return out
}

// globRE turns a wrangler rule glob into an anchored regexp: "**/" is any
// directory prefix (or none), "*" stays within one path segment, every
// other character (including "?", as in "*.wasm?module") is literal.
func globRE(g string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i += 2
		case g[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

// MatchRule returns the type of the first rule with a glob matching spec
// (an import specifier, e.g. "./doc.md").
func MatchRule(rules []Rule, spec string) (string, bool) {
	for _, r := range rules {
		for _, g := range r.Globs {
			if regexp.MustCompile(globRE(g)).MatchString(spec) {
				return r.Type, true
			}
		}
	}
	return "", false
}

// HashedName is wrangler's name for an additional module: the SHA-1 of its
// contents, a dash, and the file's base name.
func HashedName(path string, data []byte) string {
	h := sha1.Sum(data)
	return hex.EncodeToString(h[:]) + "-" + filepath.Base(path)
}

// HashedRE matches HashedName's prefix.
var HashedRE = regexp.MustCompile(`^[0-9a-f]{40}-`)

// Module is an additional module written next to the entry.
type Module struct {
	Type string // tw bundle module type (ModuleType)
	Data []byte
}

type skipRules struct{}

// rulesPlugin makes imports that match rules external modules, recorded
// in mods by output name (relative to the entry's directory).
func rulesPlugin(rules []Rule, mods map[string]Module, mu *sync.Mutex) (api.Plugin, error) {
	var res []string
	for _, r := range rules {
		if ModuleType(r.Type) == "" || r.Type == "ESModule" {
			return api.Plugin{}, fmt.Errorf("rules: type %q is not supported by tailworks' built-in bundler (Text, Data and CompiledWasm are); bundle with wrangler (tw deploy --wrangler)", r.Type)
		}
		for _, g := range r.Globs {
			res = append(res, globRE(g))
		}
	}
	filter := "(?:" + strings.Join(res, ")|(?:") + ")"
	return api.Plugin{Name: "tw-rules", Setup: func(b api.PluginBuild) {
		if len(res) == 0 {
			return
		}
		b.OnResolve(api.OnResolveOptions{Filter: filter}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if _, skip := a.PluginData.(skipRules); skip {
				return api.OnResolveResult{}, nil
			}
			typ, ok := MatchRule(rules, a.Path)
			if !ok {
				return api.OnResolveResult{}, nil
			}
			spec := strings.TrimSuffix(a.Path, "?module")
			var file string
			if strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) {
				file = filepath.Join(a.ResolveDir, spec)
				if filepath.IsAbs(spec) {
					file = spec
				}
			} else {
				r := b.Resolve(spec, api.ResolveOptions{ResolveDir: a.ResolveDir, Importer: a.Importer, Kind: a.Kind, PluginData: skipRules{}})
				if len(r.Errors) > 0 {
					return api.OnResolveResult{}, fmt.Errorf("resolve %s: %s", spec, r.Errors[0].Text)
				}
				file = r.Path
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return api.OnResolveResult{}, err
			}
			name := HashedName(file, data)
			mu.Lock()
			mods[name] = Module{Type: ModuleType(typ), Data: data}
			mu.Unlock()
			return api.OnResolveResult{Path: "./" + name, External: true}, nil
		})
	}}, nil
}
