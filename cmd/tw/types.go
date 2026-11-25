package main

// `tw types` (issue #41), the equivalent of `wrangler types`: it writes
// worker-configuration.d.ts, the TypeScript `Env` of the app's Worker,
// from its wrangler config and tailworks.jsonc, so `npx tsc --noEmit` and the
// editor know env.<NAME>. Bindings tailworks doesn't support (wranglerKeyNote)
// are left out: on tailworks they're undefined. A runtime "node" app gets
// process.env (NodeJS.ProcessEnv) instead. `--check` (CI) writes nothing
// and fails when the file is missing or stale.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// typesFile is where `tw types` writes by default (wrangler's name).
const typesFile = "worker-configuration.d.ts"

// typesExampleVars is a committed .dev.vars with placeholder values: its
// names are secrets too, so `tw types --check` in CI (no .dev.vars) sees
// the same secrets as a developer's machine.
const typesExampleVars = ".dev.vars.example"

type typesResult struct {
	File    string   `json:"file"`
	Runtime string   `json:"runtime"`
	Names   []string `json:"names"`
	Changed bool     `json:"changed"`
	Checked bool     `json:"checked,omitempty"`
}

func cmdTypes(args []string) error {
	f := newFlags("types")
	out := f.String("out", typesFile, "the declaration file to write, relative to the app directory")
	check := f.Bool("check", false, "write nothing; fail (types_stale) if the file is missing or differs from what tw types would write (for CI)")
	o := &appOpts{}
	f.StringVar(&o.Env, "env", "", "wrangler environment: type env.<name> from the wrangler config, as `tw deploy --env` deploys it")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return fail("bad_request", "tw types takes at most one directory", "usage: tw "+usageOf("types"))
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	a, err := loadAppWith(dir, "", *o)
	if err != nil {
		return err
	}
	secrets, err := typesSecrets(a)
	if err != nil {
		return err
	}
	path := *out
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.Dir, path)
	}
	src, names := genTypes(a, filepath.Dir(path), secrets)
	cur, rerr := os.ReadFile(path)
	res := typesResult{File: path, Runtime: a.Runtime, Names: names, Changed: rerr != nil || !bytes.Equal(cur, src)}
	if res.Runtime == "" {
		res.Runtime = "workerd"
	}
	if *check {
		res.Checked = true
		if res.Changed {
			why := "differs from what tw types writes now"
			if rerr != nil {
				why = "is missing"
			}
			return fail("types_stale", fmt.Sprintf("%s %s", rel(a.Dir, path), why)).withFix("tw types " + quoteDir(dir))
		}
		emit(f, res, func() { fmt.Printf("✓ %s is up to date (%d names)\n", rel(a.Dir, path), len(names)) })
		return nil
	}
	if res.Changed {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return err
		}
	}
	emit(f, res, func() {
		verb := "wrote"
		if !res.Changed {
			verb = "unchanged:"
		}
		fmt.Printf("✓ %s %s (%d names", verb, rel(a.Dir, path), len(names))
		if a.Runtime == bundle.RuntimeNode {
			fmt.Printf(" in process.env)\n")
		} else {
			fmt.Printf(" in Env)\n")
		}
		if !inTsconfig(a.Dir, path) {
			fmt.Printf("  add %q to tsconfig.json \"include\" so tsc and your editor see it\n", rel(a.Dir, path))
		}
	})
	return nil
}

// rel is p relative to dir when it's inside it, else p.
func rel(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func quoteDir(d string) string {
	if d == "." {
		return ""
	}
	return d
}

// inTsconfig reports whether tsconfig.json's "include" names the file
// (or there's no tsconfig.json to tell).
func inTsconfig(dir, path string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if err != nil {
		return true
	}
	var tc struct {
		Include []string `json:"include"`
		Files   []string `json:"files"`
	}
	if bundle.UnmarshalJSONC(b, &tc) != nil || (tc.Include == nil && tc.Files == nil) {
		return true // tsc's default include is everything
	}
	r := filepath.ToSlash(rel(dir, path))
	for _, s := range append(tc.Include, tc.Files...) {
		s = strings.TrimPrefix(filepath.ToSlash(s), "./")
		if s == r || s == "**/*" || s == "*" || strings.HasPrefix(r, strings.TrimSuffix(s, "/")+"/") {
			return true
		}
	}
	return false
}

// typesSecrets are the secret names tw types declares: .dev.vars,
// .dev.vars.example and the wrangler config's secrets.required. They are all
// local files, so the output is the same on every machine with the same
// checkout (no secrets from mgmt: `tw secret ls --json` lists those).
func typesSecrets(a *app) ([]string, error) {
	var out []string
	for _, fn := range []string{DevVarsFile, typesExampleVars} {
		b, err := os.ReadFile(filepath.Join(a.Dir, fn))
		if err != nil {
			continue
		}
		vars, err := parseDotenv(fn, string(b))
		if err != nil {
			return nil, fail("bad_request", err.Error())
		}
		for k := range vars {
			out = append(out, k)
		}
	}
	if s, ok := a.Wrangler["secrets"].(map[string]any); ok {
		req, _ := s["required"].([]any)
		for _, r := range req {
			if n, ok := r.(string); ok && n != "" {
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// typesGroup is one commented group of members.
type typesGroup struct {
	title   string
	members map[string]string // name -> TS type
}

// genTypes renders the declaration file for a, written into outDir (for
// the relative import of Durable Object classes), and the declared names.
func genTypes(a *app, outDir string, secrets []string) ([]byte, []string) {
	var groups []typesGroup
	add := func(title string, m map[string]string) {
		if len(m) > 0 {
			groups = append(groups, typesGroup{title, m})
		}
	}
	src := a.WranglerFile + " and tailworks.jsonc"
	if a.Runtime == bundle.RuntimeNode {
		src = "tailworks.jsonc"
		var top struct {
			Node struct {
				Env    map[string]string `json:"env"`
				Listen string            `json:"listen"`
			} `json:"node"`
		}
		bundle.UnmarshalJSONC(a.TwRaw, &top)
		tw := map[string]string{}
		for _, k := range []string{"TW_CALLER_KEY", "TW_DATA_DIR", "TW_DEPLOYMENT", "TW_EGRESS_PROXY", "TW_NODE_NAME", "TW_WORKLOAD"} {
			tw[k] = "string"
		}
		if top.Node.Listen == "unix" {
			tw["TW_SOCKET"] = "string"
		} else {
			tw["PORT"] = "string"
		}
		add("set by tailworks for every process", tw)
		add("tailworks.jsonc node.env", strMembers(sortedKeys(top.Node.Env)))
	} else {
		w := a.Wrangler
		vars := map[string]string{}
		if m, ok := w["vars"].(map[string]any); ok {
			for k, v := range m {
				vars[k] = tsType(v, "  ")
			}
		}
		add("vars", vars)
		dos := map[string]string{}
		if d, ok := w["durable_objects"].(map[string]any); ok {
			bs, _ := d["bindings"].([]any)
			for _, b := range bs {
				m, _ := b.(map[string]any)
				name, _ := m["name"].(string)
				cls, _ := m["class_name"].(string)
				if name == "" {
					continue
				}
				if script, _ := m["script_name"].(string); script != "" || cls == "" || !jsIdent(cls) {
					dos[name] = "DurableObjectNamespace"
					continue
				}
				dos[name] = "DurableObjectNamespace<" + mainImport(a, outDir) + "." + cls + ">"
			}
		}
		add("Durable Objects", dos)
		add("KV namespaces", bindingMembers(w["kv_namespaces"], "KVNamespace"))
		add("R2 buckets", bindingMembers(w["r2_buckets"], "R2Bucket"))
		add("D1 databases", bindingMembers(w["d1_databases"], "D1Database"))
		if q, ok := w["queues"].(map[string]any); ok {
			add("Queues (producers)", bindingMembers(q["producers"], "Queue"))
		}
		add("Workflows", bindingMembers(w["workflows"], "Workflow"))
		add("Service bindings", bindingMembers(w["services"], "Fetcher"))
		add("Static assets", bindingMembers([]any{w["assets"]}, "Fetcher"))
	}
	sec := map[string]string{}
	for _, s := range secrets {
		if jsIdent(s) {
			sec[s] = "string"
		}
	}
	add("secrets (.dev.vars, .dev.vars.example, secrets.required)", sec)

	// A name declared twice keeps its first (binding) type.
	seen := map[string]bool{}
	names := []string{}
	var body strings.Builder
	ind := "  "
	if a.Runtime == bundle.RuntimeNode {
		ind = "    "
	}
	for _, g := range groups {
		var lines []string
		for _, k := range sortedKeys(g.members) {
			if seen[k] {
				continue
			}
			seen[k] = true
			names = append(names, k)
			lines = append(lines, fmt.Sprintf("%s%s: %s;\n", ind, propName(k), g.members[k]))
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&body, "%s// %s\n", ind, g.title)
		body.WriteString(strings.Join(lines, ""))
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s.\n", typesHeader, src) // dev_types.go
	b.WriteString("// Don't edit it: run `tw types` again after changing them\n")
	b.WriteString("// (`tw types --check` fails when this file is stale).\n")
	if a.Opts.Env != "" {
		fmt.Fprintf(&b, "// Environment: --env %s.\n", a.Opts.Env)
	}
	if a.Runtime == bundle.RuntimeNode {
		b.WriteString("declare namespace NodeJS {\n  interface ProcessEnv {\n")
		b.WriteString(body.String())
		b.WriteString("  }\n}\n")
	} else {
		b.WriteString("interface Env {\n")
		b.WriteString(body.String())
		b.WriteString("}\n")
	}
	return []byte(b.String()), names
}

func strMembers(keys []string) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = "string"
	}
	return m
}

// bindingMembers maps each entry's "binding" of a wrangler binding list
// to typ.
func bindingMembers(v any, typ string) map[string]string {
	m := map[string]string{}
	list, _ := v.([]any)
	for _, e := range list {
		if o, ok := e.(map[string]any); ok {
			if b, _ := o["binding"].(string); b != "" {
				m[b] = typ
			}
		}
	}
	return m
}

// mainImport is the type-only import of the Worker's main module, relative
// to outDir: import("./src/index").
func mainImport(a *app, outDir string) string {
	main := wranglerMain(a.Wrangler)
	if main == "" {
		main = "src/index.ts"
	}
	p := filepath.Join(a.Dir, main)
	r, err := filepath.Rel(outDir, p)
	if err != nil {
		r = p
	}
	r = filepath.ToSlash(r)
	for _, ext := range []string{".ts", ".mts", ".tsx", ".js", ".mjs", ".jsx"} {
		if strings.HasSuffix(r, ext) {
			r = strings.TrimSuffix(r, ext)
			break
		}
	}
	if !strings.HasPrefix(r, ".") && !strings.HasPrefix(r, "/") {
		r = "./" + r
	}
	b, _ := json.Marshal(r)
	return "import(" + string(b) + ")"
}

var jsIdentRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func jsIdent(s string) bool { return jsIdentRE.MatchString(s) }

// propName is k as a TS property name (quoted unless an identifier).
func propName(k string) string {
	if jsIdent(k) {
		return k
	}
	b, _ := json.Marshal(k)
	return string(b)
}

// tsType is the (widened) TypeScript type of a JSON value: a var's
// string is `string`, not its literal, so a template's "" default
// doesn't type the var as "".
func tsType(v any, ind string) string {
	switch x := v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case []any:
		var ts []string
		seen := map[string]bool{}
		for _, e := range x {
			t := tsType(e, ind)
			if !seen[t] {
				seen[t] = true
				ts = append(ts, t)
			}
		}
		switch len(ts) {
		case 0:
			return "unknown[]"
		case 1:
			if strings.ContainsAny(ts[0], " |") {
				return "(" + ts[0] + ")[]"
			}
			return ts[0] + "[]"
		}
		return "(" + strings.Join(ts, " | ") + ")[]"
	case map[string]any:
		if len(x) == 0 {
			return "Record<string, never>"
		}
		var b strings.Builder
		b.WriteString("{\n")
		for _, k := range sortedKeys(x) {
			fmt.Fprintf(&b, "%s  %s: %s;\n", ind, propName(k), tsType(x[k], ind+"  "))
		}
		b.WriteString(ind + "}")
		return b.String()
	}
	return "unknown"
}
