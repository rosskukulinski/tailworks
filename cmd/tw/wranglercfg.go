package main

// Wrangler config (issue #19): tailworks reads wrangler.json, wrangler.jsonc or
// wrangler.toml, in wrangler's own precedence, resolves `--env <name>` the
// way wrangler does, and says (one line per key) which settings it ignores.
// The bundle always carries the resolved config as wrangler.jsonc (JSON),
// so tw-hostd and mgmt never see TOML or environments.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
)

// wranglerConfigNames are wrangler 4's config files, in its precedence
// order (the first that exists wins).
var wranglerConfigNames = []string{"wrangler.json", "wrangler.jsonc", "wrangler.toml"}

// findWranglerConfig returns the config file wrangler would use in dir
// ("" if none), and any other config files it would ignore.
func findWranglerConfig(dir string) (file string, shadowed []string) {
	for _, n := range wranglerConfigNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			continue
		}
		if file == "" {
			file = n
		} else {
			shadowed = append(shadowed, n)
		}
	}
	return file, shadowed
}

// readWranglerConfig parses a wrangler config file (TOML, JSON or JSONC)
// into a JSON-shaped map.
func readWranglerConfig(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if strings.HasSuffix(path, ".toml") {
		var raw map[string]any
		if _, err := toml.Decode(string(b), &raw); err != nil {
			return nil, err
		}
		cfg, _ = jsonShaped(raw).(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		return cfg, nil
	}
	if err := bundle.UnmarshalJSONC(b, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// jsonShaped converts TOML values to what encoding/json would decode:
// integers to float64, dates and times to strings.
func jsonShaped(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonShaped(e)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonShaped(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonShaped(e)
		}
		return out
	case int64:
		return float64(x)
	case time.Time:
		// TOML local dates and times (compatibility_date = 2026-04-01,
		// unquoted) decode with a location named after their kind.
		switch x.Location().String() {
		case "date-local":
			return x.Format("2006-01-02")
		case "time-local":
			return x.Format("15:04:05.999999999")
		case "datetime-local":
			return x.Format("2006-01-02T15:04:05.999999999")
		}
		return x.Format(time.RFC3339Nano)
	}
	return v
}

// wranglerNotInheritable are the keys an environment does not inherit from
// the top level (wrangler 4's notInheritable fields): an env without its
// own copy has none. Every other key is inherited unless the env sets it.
var wranglerNotInheritable = map[string]bool{
	"ai":                        true,
	"analytics_engine_datasets": true,
	"browser":                   true,
	"cloudchamber":              true,
	"containers":                true,
	"d1_databases":              true,
	"define":                    true,
	"dispatch_namespaces":       true,
	"durable_objects":           true,
	"hyperdrive":                true,
	"images":                    true,
	"kv_namespaces":             true,
	"media":                     true,
	"mtls_certificates":         true,
	"pipelines":                 true,
	"queues":                    true,
	"r2_buckets":                true,
	"ratelimits":                true,
	"secrets_store_secrets":     true,
	"send_email":                true,
	"services":                  true,
	"streaming_tail_consumers":  true,
	"tail_consumers":            true,
	"unsafe":                    true,
	"unsafe_hello_world":        true,
	"vars":                      true,
	"vectorize":                 true,
	"version_metadata":          true,
	"vpc_services":              true,
	"workflows":                 true,
	"worker_loaders":            true,
}

// wranglerTopOnly are top-level keys an environment can't set.
var wranglerTopOnly = map[string]bool{
	"env":        true,
	"keep_vars":  true,
	"dev":        true,
	"site":       true,
	"legacy_env": true,
	"$schema":    true,
}

// wranglerEnvNames lists cfg's environments, sorted.
func wranglerEnvNames(cfg map[string]any) []string {
	envs, _ := cfg["env"].(map[string]any)
	out := make([]string, 0, len(envs))
	for k := range envs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyWranglerEnv resolves environment env as wrangler does: the env's
// keys win, inheritable keys fall back to the top level, non-inheritable
// ones (vars, define, bindings) don't, and the name defaults to
// "<name>-<env>". explicitName reports whether the env set its own name;
// notes name the top-level keys the env doesn't get (wrangler warns about
// these too). env "" is the top level (with "env" dropped).
func applyWranglerEnv(cfg map[string]any, env, file string) (out map[string]any, explicitName bool, notes []string, err error) {
	out = map[string]any{}
	if env == "" {
		for k, v := range cfg {
			if k != "env" {
				out[k] = v
			}
		}
		return out, false, nil, nil
	}
	envs, _ := cfg["env"].(map[string]any)
	e, ok := envs[env].(map[string]any)
	if !ok {
		names := wranglerEnvNames(cfg)
		fix := "remove --env to deploy the top-level config"
		if len(names) > 0 {
			fix = "environments in " + file + ": " + strings.Join(names, ", ")
		}
		return nil, false, nil, fail("bad_request", fmt.Sprintf("%s has no environment %q (env.%s)", file, env, env)).withFix(fix)
	}
	for k, v := range cfg {
		if wranglerTopOnly[k] {
			continue
		}
		if wranglerNotInheritable[k] {
			if _, ok := e[k]; !ok {
				notes = append(notes, fmt.Sprintf("%s: env.%s: %s is set at the top level but not in env.%s; environments don't inherit it (as in wrangler), so env.%s has none", file, env, k, env, env))
			}
			continue
		}
		out[k] = v
	}
	sort.Strings(notes)
	if n, _ := cfg["name"].(string); n != "" {
		out["name"] = n + "-" + env
	}
	for k, v := range e {
		out[k] = v
	}
	_, explicitName = e["name"]
	return out, explicitName, notes, nil
}

// wranglerKeyNote says what tailworks does with a top-level wrangler key. Keys
// tailworks uses map to "" (no warning).
func wranglerKeyNote(k string, v any, builder string) string {
	const bindingHint = "no such binding on tailworks: "
	if bundle.IsStoreKey(k) {
		return "" // a platform store binding (ADR-044)
	}
	switch k {
	case "name", "main", "compatibility_date", "compatibility_flags", "vars", "durable_objects",
		"migrations", "triggers", "containers", "placement", "workflows", "queues", "$schema", "env":
		return ""
	case "services":
		return "" // service bindings (ADR-053): another workload of this tenant
	case "define", "alias", "rules", "minify", "keep_names", "jsx_factory", "jsx_fragment", "tsconfig":
		return "" // both bundlers honour these
	case "build", "no_bundle", "find_additional_modules", "base_dir", "preserve_file_names":
		if builder == "wrangler" || (builder == builderPrebuilt && k != "build") {
			return "" // nobundle.go packs a pre-built no_bundle Worker as wrangler does
		}
		if builder == "esbuild" && k == "build" {
			return "" // buildcmd.go runs build.command before the built-in esbuild
		}
		return "needs wrangler's bundler; ignored by tailworks' built-in esbuild (use tw deploy --wrangler)"
	case "upload_source_maps":
		if builder == "wrangler" {
			return "ignored: tailworks ships source maps only from its built-in esbuild"
		}
		return "" // sourcemap.go: false leaves the map out of tw deploy
	case "routes", "route", "workers_dev", "preview_urls", "account_id", "zone_id":
		return "ignored: there is no Cloudflare zone or workers.dev; the workload answers at its MagicDNS name (tailworks.jsonc \"public\" for the internet)"
	case "assets":
		return "" // assets.go: served by a generated router Worker
	case "pages_build_output_dir":
		return "" // pages.go: a Cloudflare Pages project's assets directory
	case "site":
		return "not supported: Workers Sites is deprecated; use \"assets\" (docs/wrangler.md#assets)"
	case "kv_namespaces", "r2_buckets", "ai", "browser", "vectorize",
		"hyperdrive", "analytics_engine_datasets", "send_email", "dispatch_namespaces",
		"mtls_certificates", "images", "pipelines", "ratelimits", "secrets_store_secrets", "version_metadata",
		"worker_loaders", "vpc_services", "media", "unsafe", "unsafe_hello_world", "cloudchamber",
		"tail_consumers", "streaming_tail_consumers", "logfwdr":
		if names := bindingNames(v); len(names) > 0 {
			return bindingHint + "env." + strings.Join(names, ", env.") + " will be undefined"
		}
		return bindingHint + "ignored"
	// limits.md#wrangler-limits-ignored
	case "dev":
		return devWranglerNote(v) // dev_port.go: tw dev honours dev.port
	case "observability", "logpush", "limits", "usage_model", "send_metrics", "keep_vars",
		"compliance_region", "first_party_worker", "legacy_env":
		return "ignored by tailworks"
	case "python_modules":
		return "ignored: tw deploy ships python_modules/ whole, less __pycache__ and *.pyc (docs/python.md#modules-and-files)"
	case "secrets":
		return "" // secrets.required: names `tw types` declares (deploy doesn't check them)
	case "node_compat":
		return "ignored: removed in wrangler 4; use compatibility_flags [\"nodejs_compat\"]"
	}
	return "unknown key, ignored"
}

// bindingNames returns the env names a binding section declares.
func bindingNames(v any) []string {
	var out []string
	add := func(m map[string]any) {
		for _, f := range []string{"binding", "name"} {
			if s, ok := m[f].(string); ok && s != "" {
				out = append(out, s)
				return
			}
		}
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				add(m)
			}
		}
	case map[string]any:
		if bs, ok := x["bindings"].([]any); ok {
			return bindingNames(bs)
		}
		add(x)
	}
	return out
}

// wranglerWarnings is one line per wrangler key tailworks doesn't use, sorted.
func wranglerWarnings(file string, cfg map[string]any, builder string) []string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if n := wranglerKeyNote(k, cfg[k], builder); n != "" {
			out = append(out, fmt.Sprintf("%s: %s: %s", file, k, n))
		}
	}
	out = append(out, containerKeyWarnings(file, cfg)...) // containerbuild.go
	if dos, ok := cfg["durable_objects"].(map[string]any); ok {
		bs, _ := dos["bindings"].([]any)
		for _, b := range bs {
			if m, ok := b.(map[string]any); ok {
				if s, _ := m["script_name"].(string); s != "" {
					out = append(out, fmt.Sprintf("%s: durable_objects binding %v: script_name (a class in another Worker) is not supported", file, m["name"]))
				}
			}
		}
	}
	return out
}

// esbuildWranglerOptions maps wrangler's build settings onto the built-in
// bundler's options.
func esbuildWranglerOptions(w map[string]any, o *esbundle.Options) error {
	strMap := func(k string) (map[string]string, error) {
		m, ok := w[k].(map[string]any)
		if !ok {
			return nil, nil
		}
		out := map[string]string{}
		for kk, v := range m {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%s: want a string", k, kk)
			}
			out[kk] = s
		}
		return out, nil
	}
	var err error
	if o.Define, err = strMap("define"); err != nil {
		return err
	}
	if o.Alias, err = strMap("alias"); err != nil {
		return err
	}
	if rs, ok := w["rules"]; ok {
		b, _ := json.Marshal(rs)
		if err := json.Unmarshal(b, &o.Rules); err != nil {
			return fmt.Errorf("rules: %w", err)
		}
	}
	if m, _ := w["minify"].(bool); m {
		o.Minify = true
	}
	o.KeepNames, _ = w["keep_names"].(bool)
	o.JSXFactory, _ = w["jsx_factory"].(string)
	o.JSXFragment, _ = w["jsx_fragment"].(string)
	if ts, _ := w["tsconfig"].(string); ts != "" {
		o.Tsconfig = ts
	}
	o.NodeCompat = nodeCompat(w) // requirebuiltins.go
	return nil
}

// defaultTwConfig is the tailworks.jsonc used when an app has only a wrangler
// config: the workload node tagged tag:tw-wl-<name>. Every Durable Object
// class is made internal ("node": false) after, as for any tailworks.jsonc
// that leaves it out of tailnet.objects (defaultInternalClasses).
func defaultTwConfig(name string) []byte {
	b, _ := json.MarshalIndent(map[string]any{"name": name, "tailnet": map[string]any{"tags": []string{"tag:tw-wl-" + name}}}, "", "  ")
	return b
}

// wranglerMain is the config's main ("" if unset).
func wranglerMain(w map[string]any) string {
	m, _ := w["main"].(string)
	return m
}
