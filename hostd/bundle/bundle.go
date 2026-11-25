// Package bundle reads and writes tailworks deployment bundles: a tar of the built
// worker modules plus tailworks.jsonc and wrangler.jsonc (contract: POST
// /v1/deployments), and parses the parts of both configs tw-hostd uses.
package bundle

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/cron"
	"github.com/rosskukulinski/tailworks/internal/publicspec"
	"github.com/rosskukulinski/tailworks/internal/queuecfg"
	"github.com/rosskukulinski/tailworks/internal/svcbind"
)

// MaxBundle bounds an unpacked bundle.
const MaxBundle = 64 << 20

// Egress modes per DO class (tailworks.jsonc tailnet.objects.<Class>.egress).
const (
	EgressObject   = "object"   // facet mode: egress leaves from the object's own node (default)
	EgressWorkload = "workload" // plain-DO mode: alarms work; egress is attributed to the workload node
)

// App is a parsed bundle.
type App struct {
	Name          string
	Main          string // module path of the entry module
	CompatDate    string
	CompatFlags   []string
	Vars          map[string]json.RawMessage
	Bindings      []DOBinding       // durable_objects.bindings, in order
	Classes       map[string]*Class // by class name
	WorkloadTags  []string
	Modules       map[string][]byte // path -> contents (js/mjs/json/txt/wasm)
	SpawnMaxLive  int
	SpawnMaxDepth int
	Internet      InternetEgress // tailnet.egress.internet: Workers' and containers' internet egress (internet.go)
	// EgressVia is tailnet.egress.via: "" or ViaExitNode (the workload needs
	// a host whose internet path is an exit node; internet.go).
	EgressVia string

	// TailnetHTTPS is tailnet.https: the workload node also serves :443
	// with its ts.net certificate, and requests that arrive that way get
	// an https:// request.url (#360, hostd/tailnethttps.go).
	TailnetHTTPS bool

	// Tier is the requested isolation tier (tailworks.jsonc "tier"): "" (host
	// default), "process" or "microvm".
	Tier string
	// Runtime is "" (workerd) or RuntimeNode (tailworks.jsonc "runtime": "node").
	Runtime string
	// Node is the Node process tier app (Runtime == RuntimeNode).
	Node *NodeApp
	// Containers are the container classes (wrangler "containers", with
	// tailworks.jsonc "containers" overriding or pinning by class), by DO class:
	// each instance of the class may run one container (ADR-032).
	Containers map[string]container.Class
	// Public (tailworks.jsonc "public", ADR-029) lists the path prefixes reachable
	// from the public internet over Funnel, and their limits. nil: the
	// workload is tailnet-only.
	Public *publicspec.Spec
	// Crons are the cron triggers (wrangler.jsonc triggers.crons), each a
	// valid internal/cron expression, run against the default export's
	// scheduled() handler.
	Crons []string

	// Secrets are the workload's secret values (`tw secret put`,
	// ADR-034), never in the bundle: hostd fills them in from
	// mgmt's lease-fenced grant when it starts a deployment. They become
	// text bindings (workerd) or environment variables (runtime node) and
	// override vars of the same name. Never logged or serialised.
	Secrets map[string]string `json:"-"`

	// ModuleTypes are module types the bundler set explicitly
	// (tw-bundle.json "modules": path -> "js" | "json" | "text" | "data"
	// | "wasm"), e.g. a wrangler "rules" Text module named *.md. Modules
	// not listed are typed by extension.
	ModuleTypes map[string]string

	// Stores are the platform storage bindings (kv_namespaces, …;
	// stores.go, ADR-044), in wrangler order.
	Stores []StoreBinding

	// Queues is wrangler.jsonc "queues" (queues.go, ADR-039); nil: none.
	Queues *queuecfg.Config

	// Workflows are wrangler.jsonc "workflows" (workflows.go), run by
	// workerd's built-in engine.
	Workflows []Workflow

	// Assets is wrangler.jsonc "assets" and its files (assets.go); nil: none.
	Assets *Assets

	// Services are wrangler.jsonc "services" (services.go,
	// ADR-053), in wrangler order.
	Services []svcbind.Binding

	// SourceMaps are the bundler's source maps (<module>.map, e.g.
	// dist/index.js.map), not modules: hostd maps exception stacks in
	// request records with them (hostd/stackmap.go).
	SourceMaps map[string][]byte

	// Python: main is a .py module (python.go). hostd runs the modules in
	// a Python worker behind a JavaScript front.
	Python bool
}

// RuntimeNode is the opt-in Node.js process tier (tailworks.jsonc "runtime": "node").
const RuntimeNode = "node"

// NodeApp is a Node process tier deployment: one supervised `node` process
// with one tailnet identity (the workload node), per ADR-022.
type NodeApp struct {
	Main   string            // entry module, relative to the bundle root
	Files  map[string][]byte // every bundle file (written to the instance dir)
	Env    map[string]string // tailworks.jsonc node.env plus deploy vars
	Listen string            // "tcp" (PORT on 127.0.0.1, default) or "unix" (TW_SOCKET)
	Health string            // health path (default "/"): any HTTP response means up
	Args   []string          // extra node CLI flags (node.args)
}

// Isolation tiers.
const (
	TierProcess = "process"
	TierMicroVM = "microvm"
)

// DOBinding is one env binding to a DO namespace.
type DOBinding struct {
	Name  string
	Class string
}

// Class is one DO class's tailworks settings.
type Class struct {
	Name   string
	Tags   []string
	Egress string // EgressObject | EgressWorkload
	TTL    string
	// Node is false for internal classes (`"node": false`): no tailnet node,
	// reachable only in-process; implies plain-DO mode.
	Node bool
	// Public (tailnet.objects.<Class>.public) lets public requests create
	// objects of this class, up to MaxObjects (ADR-029 D5).
	Public *publicspec.ClassSpec
}

// Facet reports whether the class runs in facet mode.
func (c *Class) Facet() bool { return c.Egress != EgressWorkload }

type wranglerConfig struct {
	Name               string                     `json:"name"`
	Main               string                     `json:"main"`
	CompatibilityDate  string                     `json:"compatibility_date"`
	CompatibilityFlags []string                   `json:"compatibility_flags"`
	Vars               map[string]json.RawMessage `json:"vars"`
	Triggers           struct {
		Crons []string `json:"crons"`
	} `json:"triggers"`
	DurableObjects struct {
		Bindings []struct {
			Name       string `json:"name"`
			ClassName  string `json:"class_name"`
			ScriptName string `json:"script_name"`
		} `json:"bindings"`
	} `json:"durable_objects"`
}

type twConfig struct {
	Name    string           `json:"name"`
	Public  *publicspec.Spec `json:"public"`
	Tier    string           `json:"tier"`
	Runtime string           `json:"runtime"`
	Main    string           `json:"main"`
	Node    struct {
		Env    map[string]string `json:"env"`
		Listen string            `json:"listen"`
		Health string            `json:"health"`
		Args   []string          `json:"args"`
	} `json:"node"`
	// Containers is parsed by internal/container (only its presence here):
	// an array like wrangler's, or {"<Class>": {"image", "images"}},
	// per-class image refs you pin; mgmt records the digests it resolves
	// in the deployment (ADR-032 D3).
	Containers json.RawMessage `json:"containers"`
	Tailnet    struct {
		Tags     []string `json:"tags"`
		Connects []string `json:"connects"`
		Objects  map[string]struct {
			Tags   []string              `json:"tags"`
			Egress string                `json:"egress"`
			TTL    string                `json:"ttl"`
			Node   *bool                 `json:"node"`
			Public *publicspec.ClassSpec `json:"public"`
		} `json:"objects"`
		Spawn struct {
			MaxLive  int `json:"maxLive"`
			MaxDepth int `json:"maxDepth"`
		} `json:"spawn"`
		Egress struct {
			Internet InternetEgress `json:"internet"`
			Via      string         `json:"via"`
		} `json:"egress"`

		// HTTPS is tailnet.https (App.TailnetHTTPS).
		HTTPS bool `json:"https"`
	} `json:"tailnet"`
}

var (
	identRE    = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	workloadRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
)

// ValidWorkload reports whether s is a usable workload name.
func ValidWorkload(s string) bool { return workloadRE.MatchString(s) }

// Parse parses an unpacked bundle (path -> contents).
func Parse(files map[string][]byte) (*App, error) {
	if tb, ok := files["tailworks.jsonc"]; ok {
		var t twConfig
		if err := UnmarshalJSONC(tb, &t); err != nil {
			return nil, fmt.Errorf("bundle: tailworks.jsonc: %w", err)
		}
		switch t.Runtime {
		case "", "workerd":
		case RuntimeNode:
			if crons, err := wranglerCrons(files); err != nil || len(crons) > 0 {
				if err != nil {
					return nil, err
				}
				return nil, errors.New("bundle: triggers.crons: cron triggers need the workerd runtime (a scheduled() handler); runtime \"node\" does not support them")
			}
			if err := refuseNodeQueues(files); err != nil {
				return nil, err
			}
			return parseNode(files, &t)
		default:
			return nil, fmt.Errorf("bundle: tailworks.jsonc runtime %q: want \"workerd\" (default) or \"node\"", t.Runtime)
		}
	}
	wb, ok := files["wrangler.jsonc"]
	if !ok {
		wb, ok = files["wrangler.json"]
	}
	if !ok {
		return nil, errors.New("bundle: wrangler.jsonc missing")
	}
	var w wranglerConfig
	if err := UnmarshalJSONC(wb, &w); err != nil {
		return nil, fmt.Errorf("bundle: wrangler.jsonc: %w", err)
	}
	var t twConfig
	if tb, ok := files["tailworks.jsonc"]; ok {
		if err := UnmarshalJSONC(tb, &t); err != nil {
			return nil, fmt.Errorf("bundle: tailworks.jsonc: %w", err)
		}
	}
	a := &App{
		Name: w.Name, CompatDate: w.CompatibilityDate,
		CompatFlags: w.CompatibilityFlags, Vars: w.Vars, Classes: map[string]*Class{},
		WorkloadTags: t.Tailnet.Tags, Modules: map[string][]byte{},
		SpawnMaxLive: t.Tailnet.Spawn.MaxLive, SpawnMaxDepth: t.Tailnet.Spawn.MaxDepth,
		Internet: t.Tailnet.Egress.Internet, Tier: t.Tier, Public: t.Public,
		EgressVia: t.Tailnet.Egress.Via,
	}
	a.TailnetHTTPS = t.Tailnet.HTTPS
	if err := a.Public.Validate(); err != nil {
		return nil, fmt.Errorf("bundle: tailworks.jsonc %w", err)
	}
	if err := cron.ValidateTriggers(w.Triggers.Crons); err != nil {
		return nil, fmt.Errorf("bundle: wrangler.jsonc: %w", err)
	}
	a.Crons = w.Triggers.Crons
	switch a.Tier {
	case "", TierProcess, TierMicroVM:
	default:
		return nil, fmt.Errorf("bundle: tailworks.jsonc tier %q: want \"process\" or \"microvm\"", a.Tier)
	}
	switch a.EgressVia {
	case "", ViaExitNode:
	default:
		return nil, fmt.Errorf("bundle: tailworks.jsonc tailnet.egress.via %q: want %q", a.EgressVia, ViaExitNode)
	}
	if t.Name != "" {
		a.Name = t.Name
	}
	// tw-bundle.json (written by the bundler) points main at the built module.
	if bb, ok := files["tw-bundle.json"]; ok {
		var o struct {
			Main    string            `json:"main"`
			Modules map[string]string `json:"modules"`
		}
		if err := json.Unmarshal(bb, &o); err != nil {
			return nil, fmt.Errorf("bundle: tw-bundle.json: %w", err)
		}
		if o.Main != "" {
			w.Main = o.Main
		}
		if err := a.setModuleTypes(files, o.Modules); err != nil {
			return nil, err
		}
	}
	if a.CompatDate == "" {
		a.CompatDate = "2026-04-01"
	}
	assets := hasAssetsConfig(StripJSONC(wb)) // assets.go: assets/ holds files, not modules
	if w.Main == "" && !assets {
		return nil, errors.New("bundle: wrangler.jsonc has no main")
	}
	assetsOnly := w.Main == "" // an assets-only project: hostd supplies the Worker
	if assetsOnly {
		w.Main = AssetsOnlyMain
	}
	a.Main = path.Clean(strings.TrimPrefix(w.Main, "./"))
	for p, b := range files {
		if assets && strings.HasPrefix(p, AssetsPrefix) {
			continue
		}
		switch strings.ToLower(path.Ext(p)) {
		case ".js", ".mjs", ".cjs", ".json", ".txt", ".wasm", ".html", ".bin", ".py":
			if p == "wrangler.json" || p == "package.json" || p == "tw-bundle.json" || strings.HasPrefix(p, "node_modules/") {
				continue
			}
			a.Modules[p] = b
		case ".map":
			a.addSourceMap(p, b) // sourcemaps.go
		default:
			if _, typed := a.ModuleTypes[p]; typed {
				a.Modules[p] = b
			}
		}
	}
	if assetsOnly {
		a.Modules[AssetsOnlyMain] = []byte(assetsOnlyJS)
	}
	if _, ok := a.Modules[a.Main]; !ok {
		return nil, fmt.Errorf("bundle: main module %q not in bundle", a.Main)
	}
	for _, b := range w.DurableObjects.Bindings {
		if b.ScriptName != "" {
			return nil, fmt.Errorf("bundle: durable_objects binding %s: script_name is not supported", b.Name)
		}
		if !identRE.MatchString(b.Name) || !identRE.MatchString(b.ClassName) {
			return nil, fmt.Errorf("bundle: bad durable_objects binding %q -> %q", b.Name, b.ClassName)
		}
		if strings.HasPrefix(b.ClassName, "Host_") || strings.HasPrefix(b.Name, "__TW") {
			return nil, fmt.Errorf("bundle: names starting with Host_/__TW are reserved (%s)", b.Name)
		}
		a.Bindings = append(a.Bindings, DOBinding{Name: b.Name, Class: b.ClassName})
		if a.Classes[b.ClassName] == nil {
			a.Classes[b.ClassName] = &Class{Name: b.ClassName, Egress: EgressObject, Node: true}
		}
	}
	for name, o := range t.Tailnet.Objects {
		c := a.Classes[name]
		if c == nil {
			return nil, fmt.Errorf("bundle: tailworks.jsonc tailnet.objects.%s has no durable_objects binding", name)
		}
		c.Tags, c.TTL = o.Tags, o.TTL
		if err := o.Public.Validate(name); err != nil {
			return nil, fmt.Errorf("bundle: tailworks.jsonc %w", err)
		}
		c.Public = o.Public
		if o.Node != nil && !*o.Node {
			c.Node = false
			if o.Egress == EgressObject {
				return nil, fmt.Errorf("bundle: tailnet.objects.%s: \"node\": false needs \"egress\": \"workload\"", name)
			}
			o.Egress = EgressWorkload
		}
		switch o.Egress {
		case "", EgressObject:
			c.Egress = EgressObject
		case EgressWorkload:
			c.Egress = EgressWorkload
		default:
			return nil, fmt.Errorf("bundle: tailnet.objects.%s.egress must be %q or %q", name, EgressObject, EgressWorkload)
		}
	}
	stores, err := parseStores(StripJSONC(wb), a)
	if err != nil {
		return nil, err
	}
	a.Stores = stores
	if a.Queues, err = parseQueues(StripJSONC(wb), a); err != nil {
		return nil, err
	}
	if err := parseWorkflows(wb, a); err != nil {
		return nil, err
	}
	if err := parseAssets(StripJSONC(wb), files, a); err != nil { // assets.go
		return nil, err
	}
	if err := parseServices(StripJSONC(wb), a); err != nil { // services.go
		return nil, err
	}
	var twJSON []byte
	if tb, ok := files["tailworks.jsonc"]; ok {
		twJSON = StripJSONC(tb)
	}
	cs, err := container.Parse(StripJSONC(wb), twJSON)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	for _, c := range cs {
		if a.Classes[c.Class] == nil {
			return nil, fmt.Errorf("bundle: containers.%s has no durable_objects binding (a container belongs to a Durable Object class)", c.Class)
		}
		if a.Containers == nil {
			a.Containers = map[string]container.Class{}
		}
		a.Containers[c.Class] = c
	}
	if err := finishPython(a); err != nil { // python.go
		return nil, err
	}
	return a, nil
}

// wranglerCrons returns wrangler.jsonc's (or .json's) triggers.crons, if
// the bundle has one.
func wranglerCrons(files map[string][]byte) ([]string, error) {
	wb, ok := files["wrangler.jsonc"]
	if !ok {
		if wb, ok = files["wrangler.json"]; !ok {
			return nil, nil
		}
	}
	var w wranglerConfig
	if err := UnmarshalJSONC(wb, &w); err != nil {
		return nil, fmt.Errorf("bundle: wrangler.jsonc: %w", err)
	}
	return w.Triggers.Crons, nil
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseNode parses a Node process tier bundle: tailworks.jsonc (runtime "node")
// plus the built app files; main comes from tw-bundle.json or tailworks.jsonc.
func parseNode(files map[string][]byte, t *twConfig) (*App, error) {
	a := &App{
		Name: t.Name, Runtime: RuntimeNode, Classes: map[string]*Class{}, Modules: map[string][]byte{},
		WorkloadTags: t.Tailnet.Tags, Internet: t.Tailnet.Egress.Internet, Tier: t.Tier,
		TailnetHTTPS: t.Tailnet.HTTPS,
	}
	if t.Public != nil {
		return nil, errors.New("bundle: tailworks.jsonc \"public\" is not supported for runtime \"node\" yet (ADR-029 v1: workerd workloads only)")
	}
	if len(t.Tailnet.Objects) > 0 {
		return nil, errors.New("bundle: runtime \"node\" has one tailnet identity per process (ADR-022); tailnet.objects is not supported")
	}
	if c := bytes.TrimSpace(t.Containers); len(c) > 0 && string(c) != "null" && string(c) != "[]" && string(c) != "{}" {
		return nil, errors.New("bundle: runtime \"node\" has no Durable Objects, so no containers (a container belongs to a Durable Object class)")
	}
	switch a.Tier {
	case "", TierProcess:
	case TierMicroVM:
		return nil, errors.New("bundle: runtime \"node\" runs in the process tier only (the microVM guest has no Node.js yet)")
	default:
		return nil, fmt.Errorf("bundle: tailworks.jsonc tier %q: want \"process\"", a.Tier)
	}
	main := t.Main
	if bb, ok := files["tw-bundle.json"]; ok {
		var o struct {
			Main string            `json:"main"`
			Env  map[string]string `json:"env"`
		}
		if err := json.Unmarshal(bb, &o); err != nil {
			return nil, fmt.Errorf("bundle: tw-bundle.json: %w", err)
		}
		if o.Main != "" {
			main = o.Main
		}
		for k, v := range o.Env {
			if t.Node.Env == nil {
				t.Node.Env = map[string]string{}
			}
			t.Node.Env[k] = v
		}
	}
	if main == "" {
		return nil, errors.New("bundle: runtime \"node\" needs \"main\" in tailworks.jsonc (or tw-bundle.json)")
	}
	main = path.Clean(strings.TrimPrefix(main, "./"))
	if _, ok := files[main]; !ok {
		return nil, fmt.Errorf("bundle: main module %q not in bundle", main)
	}
	for k := range t.Node.Env {
		if !envKeyRE.MatchString(k) || strings.HasPrefix(k, "TW_") {
			return nil, fmt.Errorf("bundle: node.env %q: invalid name (TW_* is reserved)", k)
		}
	}
	for _, f := range t.Node.Args {
		if !strings.HasPrefix(f, "--") || strings.ContainsAny(f, " \n") {
			return nil, fmt.Errorf("bundle: node.args %q: only --flags are allowed", f)
		}
	}
	switch t.Node.Listen {
	case "":
		t.Node.Listen = "tcp"
	case "tcp", "unix":
	default:
		return nil, fmt.Errorf("bundle: node.listen %q: want \"tcp\" or \"unix\"", t.Node.Listen)
	}
	h := t.Node.Health
	if h == "" {
		h = "/"
	}
	if !strings.HasPrefix(h, "/") {
		return nil, errors.New("bundle: node.health must be a path")
	}
	a.Main = main
	a.Node = &NodeApp{Main: main, Files: files, Env: t.Node.Env, Listen: t.Node.Listen, Health: h, Args: t.Node.Args}
	return a, nil
}

// ClassNames returns the class names sorted.
func (a *App) ClassNames() []string {
	out := make([]string, 0, len(a.Classes))
	for n := range a.Classes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Unpack reads a tar into memory, rejecting unsafe paths and oversize bundles.
func Unpack(b []byte) (map[string][]byte, error) {
	tr := tar.NewReader(bytes.NewReader(b))
	out := map[string][]byte{}
	total := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("bundle: %s: only regular files are allowed", h.Name)
		}
		p := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if p == "." || strings.HasPrefix(p, "../") || p == ".." || path.IsAbs(p) || strings.Contains(p, "\\") || strings.ContainsAny(p, "\"\n\r\x00") {
			return nil, fmt.Errorf("bundle: unsafe path %q", h.Name)
		}
		total += int(h.Size)
		if total > MaxBundle {
			return nil, errors.New("bundle: too large")
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return nil, err
		}
		out[p] = data
	}
	return out, nil
}

// Pack writes files as a tar (sorted, deterministic).
func Pack(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadDir loads files from dir for Pack, keeping only the given relative
// paths (directories are walked).
func ReadDir(dir string, paths ...string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, p := range paths {
		root := filepath.Join(dir, p)
		err := filepath.WalkDir(root, func(fp string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dir, fp)
			b, err := os.ReadFile(fp)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UnmarshalJSONC decodes JSON with // and /* */ comments and trailing commas.
func UnmarshalJSONC(b []byte, v any) error {
	return json.Unmarshal(StripJSONC(b), v)
}

// StripJSONC removes comments and trailing commas outside strings.
func StripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			// drop a trailing comma before this close
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
