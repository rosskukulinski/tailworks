package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/twd/hostd/bundle"
	"github.com/rosskukulinski/twd/hostd/mgmtapi"
	"github.com/rosskukulinski/twd/mgmt"
)

// ---- init ----

var labelRE = regexp.MustCompile(`[^a-z0-9-]+`)

func workloadLabel(s string) string {
	s = labelRE.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(regexp.MustCompile(`-+`).ReplaceAllString(s, "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

func cmdInit(args []string) error {
	f := newFlags("init")
	tmpl := f.String("template", "hello", "hello | agent | mcp | ai-agent | lock | refund-approval | node-workflow")
	name := f.String("name", "", "workload name (default: the directory name)")
	keepTags := f.Bool("keep-tags", false, "keep the template's tags instead of naming them after the app (tag:twd-wl-<workload>, tag:twd-<workload>-<class>)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "twd init needs a directory", "usage: twd "+usageOf("init"))
	}
	dir, _ := filepath.Abs(pos[0])
	ct, err := createTwdDir()
	if err != nil {
		return err
	}
	src := filepath.Join(ct, "templates", *tmpl)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		ents, _ := os.ReadDir(filepath.Join(ct, "templates"))
		var av []string
		for _, e := range ents {
			av = append(av, e.Name())
		}
		return fail("bad_request", fmt.Sprintf("unknown template %q", *tmpl), "available: "+strings.Join(av, ", "))
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return fail("bad_request", dir+" exists and is not empty")
	}
	wl := *name
	if wl == "" {
		wl = filepath.Base(dir)
	}
	wl = workloadLabel(wl)
	if wl == "" {
		return fail("bad_request", "can't derive a workload name from "+dir).withFix("twd init <dir> --name <workload>")
	}
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		return err
	}
	// npm-style: templates ship "gitignore" (npm drops .gitignore files).
	if _, err := os.Stat(filepath.Join(dir, "gitignore")); err == nil {
		os.Rename(filepath.Join(dir, "gitignore"), filepath.Join(dir, ".gitignore"))
	}
	// Name the workload after the directory (twd.jsonc and wrangler.jsonc),
	// and the npm package too (its first "name" is the top-level one).
	for _, fn := range []string{"package.json", "twd.jsonc", "wrangler.jsonc"} {
		p := filepath.Join(dir, fn)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`("name"\s*:\s*)"[^"]*"`)
		done := false
		b = re.ReplaceAllFunc(b, func(m []byte) []byte {
			if done {
				return m
			}
			done = true
			return re.ReplaceAll(m, []byte(`${1}"`+wl+`"`))
		})
		if fn == "wrangler.jsonc" {
			// A template var that names the app (refund-approval's approve hint).
			b = regexp.MustCompile(`("APP_NAME"\s*:\s*)"[^"]*"`).ReplaceAll(b, []byte(`${1}"`+wl+`"`))
		}
		if fn == "twd.jsonc" && !*keepTags {
			if nb, _, err := appTags(b, wl); err == nil {
				b = nb
			}
		}
		os.WriteFile(p, b, 0o644)
	}
	// The template's workload tag (tailnet.tags, e.g. tag:twd-wl-hello)
	// follows the workload name too, so the policy snippet `twd deploy`
	// prints names this app, not the template it came from.
	if p := filepath.Join(dir, "twd.jsonc"); true {
		if b, err := os.ReadFile(p); err == nil {
			if m := regexp.MustCompile(`"tags"\s*:\s*\[\s*"(tag:twd-wl-[A-Za-z0-9._-]+)"`).FindSubmatch(b); m != nil {
				if from, to := string(m[1]), "tag:twd-wl-"+wl; from != to {
					os.WriteFile(p, bytes.ReplaceAll(b, []byte(from), []byte(to)), 0o644)
				}
			}
		}
	}
	res := map[string]string{"dir": dir, "template": *tmpl, "workload": wl}
	emit(f, res, func() {
		fmt.Printf("✓ created %s from template %s (workload %q)\n", dir, *tmpl, wl)
		if tags := initTags(dir); len(tags) > 0 {
			fmt.Printf("  tags: %s (twd deploy prints the tagOwners to add)\n", strings.Join(tags, ", "))
		}
		if *tmpl == "node-workflow" {
			// The Node tier bundles the app's own dependencies with the
			// machine's Node.js; nothing is vendored for it.
			fmt.Printf("  cd %s && npm install && twd dev    # or twd deploy\n", pos[0])
			fmt.Printf("  needs Node.js ≥ 24 and npm on this machine; `npm install` fetches the template's dependencies, so this one can't be built or deployed offline\n")
			return
		}
		fmt.Printf("  cd %s && twd deploy        # or `twd dev` for a local run with no tailnet\n", pos[0])
	})
	return nil
}

// ---- app + build ----

type app struct {
	Dir      string
	Name     string
	Cfg      *mgmt.TwdConfig
	Wrangler map[string]any
	TwdRaw   []byte
	// Runtime is "node" for the Node process tier (no wrangler config).
	Runtime string

	// Wrangler config (wranglerapp.go): the file read (wrangler.json,
	// .jsonc or .toml), the flags, and one line per ignored setting.
	WranglerFile string
	Opts         appOpts
	Warnings     []string
}

func loadApp(dir, name string) (*app, error) { return loadAppWith(dir, name, appOpts{}) }

func loadAppWith(dir, name string, o appOpts) (*app, error) {
	dir, _ = filepath.Abs(dir)
	a := &app{Dir: dir, Opts: o}
	raw, twdErr := os.ReadFile(filepath.Join(dir, "twd.jsonc"))
	var top struct {
		Name    string `json:"name"`
		Runtime string `json:"runtime"`
	}
	if twdErr == nil {
		bundle.UnmarshalJSONC(raw, &top)
	}
	a.Runtime = top.Runtime
	a.Wrangler = map[string]any{}
	wname := top.Name
	if a.Runtime != bundle.RuntimeNode {
		var err error
		if wname, err = a.loadWrangler(top.Name); err != nil {
			if _, none := err.(errNoWrangler); !none {
				return nil, err
			}
			if twdErr != nil {
				return nil, fail("bad_request", "no twd.jsonc or wrangler config in "+dir).withFix("twd init <dir> --template hello|agent|mcp|ai-agent|lock|refund-approval|node-workflow")
			}
			return nil, fail("bad_request", "no wrangler.jsonc, wrangler.json or wrangler.toml in "+dir)
		}
	} else if o.Env != "" || o.Wrangler || o.WranglerOut != "" {
		return nil, fail("bad_request", `--env, --wrangler and --from-wrangler-out need a wrangler (workerd) app; this one is runtime "node"`)
	}
	a.Name = name
	if a.Name == "" {
		a.Name = wname
	}
	if a.Name == "" {
		a.Name = filepath.Base(dir)
	}
	if twdErr != nil {
		// A wrangler-only project: twd's defaults stand in for twd.jsonc.
		raw = defaultTwdConfig(workloadLabel(a.Name), a.Wrangler)
		a.Warnings = append(a.Warnings, fmt.Sprintf("no twd.jsonc: using defaults (workload node tag:twd-wl-%s; Durable Object classes internal, no node of their own); add a twd.jsonc to change them", workloadLabel(a.Name)))
	}
	a.TwdRaw = raw
	if err := checkTwdSchema(raw); err != nil {
		return nil, err
	}
	var err error
	if a.Cfg, err = mgmt.ParseTwdConfig(raw); err != nil {
		return nil, fail("bad_request", err.Error())
	}
	if wl := workloadLabel(a.Name); wl != a.Name {
		return nil, fail("bad_request", fmt.Sprintf("workload name %q is not a DNS label", a.Name)).withFix("twd deploy --name " + wl)
	}
	if len(a.Cfg.Tailnet.Tags) == 0 {
		return nil, fail("bad_request", "twd.jsonc has no tailnet.tags (the workload node's tags)").
			withFix(`add "tailnet": { "tags": ["tag:twd-wl-` + a.Name + `"] }`)
	}
	return a, nil
}

// srcTags are every tag this app's nodes will carry.
func (a *app) srcTags() []string {
	var out []string
	for _, c := range a.Cfg.Classes() {
		for _, t := range c.Tags {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// build bundles the app with esbuild, in-process (internal/esbundle, the Go
// twin of create-twd's build.mjs), into what hostd/gen expects
// (dist/index.js + twd.jsonc + wrangler.jsonc + twd-bundle.json), and packs it.
func (a *app) build(vars map[string]string) ([]byte, int, error) {
	ct, err := createTwdDir()
	if err != nil {
		return nil, 0, err
	}
	out, err := os.MkdirTemp("", "twd-build-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(out)
	if a.Runtime == bundle.RuntimeNode {
		// The opt-in Node tier (M9) bundles with create-twd's build-node.mjs,
		// so it needs Node.js on the deploying machine; the workerd path does not.
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, 0, fail("node_required", `runtime "node" apps need Node.js on this machine to build`).
				withFix("install Node.js 24+ (e.g. https://nodejs.org) and re-run twd deploy")
		}
		return a.buildNode(node, ct, out, vars)
	}
	// In-process esbuild (Go API): no Node.js needed on this machine;
	// or wrangler's own bundler (--wrangler, --from-wrangler-out).
	var files map[string][]byte
	var types map[string]string
	var js int
	main := "dist/index.js"
	if a.Opts.builder() == "wrangler" {
		files, main, types, js, err = a.buildWrangler(out)
	} else {
		files, types, js, err = a.buildEsbuild(out, nodePaths(ct))
	}
	if err != nil {
		return nil, 0, err
	}
	tb, err := a.packWorkerd(files, main, types, vars)
	return tb, js, err
}

// buildNode bundles a runtime "node" app with create-twd's build-node.mjs
// (dist/server.mjs, plus the Vercel Workflow bundles when workflows/ exists).
func (a *app) buildNode(node, ct, out string, vars map[string]string) ([]byte, int, error) {
	args := []string{filepath.Join(ct, "lib", "build-node.mjs"), a.Dir, filepath.Join(out, "app")}
	for k, v := range vars {
		args = append(args, "--var", k+"="+v)
	}
	cmd := exec.Command(node, args...)
	cmd.Dir = a.Dir
	cmd.Env = append(os.Environ(), "TWD_NODE_PATH="+strings.Join(nodePaths(ct), ":"))
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, 0, fail("build_failed", "build-node failed for "+a.Dir, strings.Split(strings.TrimSpace(string(b)), "\n")...).
			withFix("cd " + a.Dir + " && npm install")
	}
	files, err := bundle.ReadDir(filepath.Join(out, "app"), ".")
	if err != nil {
		return nil, 0, err
	}
	tb, err := bundle.Pack(files)
	return tb, len(files["dist/server.mjs"]), err
}

// ---- deploy ----

type varFlags map[string]string

func (v varFlags) String() string { return fmt.Sprint(map[string]string(v)) }
func (v varFlags) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("--var wants KEY=VALUE")
	}
	v[k] = val
	return nil
}

type deployResult struct {
	Workload     string                  `json:"workload"`
	Tenant       string                  `json:"tenant"`
	TenantID     string                  `json:"tenantID"`
	DeploymentID string                  `json:"deploymentID"`
	Host         string                  `json:"host"`
	Status       string                  `json:"status"`
	Endpoint     any                     `json:"endpoint"`
	Preflight    mgmt.PreflightResult    `json:"preflight"`
	Placement    *mgmt.PlacementDecision `json:"placement,omitempty"`
	BundleBytes  int                     `json:"bundleBytes"`
	// Public (ADR-029) is the workload's public URL and state, if any.
	Public  *mgmt.DeploymentPublicView `json:"public,omitempty"`
	Timings map[string]float64         `json:"timingsMs"`

	// Replicas (twd.jsonc "replicas" > 1): every copy and its host.
	Replicas []mgmt.ReplicaRef `json:"replicas,omitempty"`

	// Warnings: wrangler config twd ignores, one line per key.
	Warnings []string `json:"warnings,omitempty"`
}

func cmdDeploy(args []string) error {
	f := newFlags("deploy")
	name := f.String("name", "", "workload name (default: twd.jsonc name)")
	allowMissing := f.Bool("allow-missing-grants", false, "deploy even if tailnet.connects grants are missing")
	noWait := f.Bool("no-wait", false, "return once mgmt accepted the deploy")
	forceTakeover := f.Bool("force-takeover", false, "the workload's current host is gone for good: move its Durable Objects anyway (audited; see takeover_unconfirmed); with --replace, confirms moving a stateful workload off a live host")
	platform := f.String("platform", "", "containers: arches to build Dockerfile images for (linux/amd64,linux/arm64; default: the arches of the tenant pool's container hosts)")
	hostF := f.String("host", "", "pin the workload to this host (it must be live, in the tenant's pool and offer the tier)")
	replace := f.Bool("replace", false, "re-place the workload: move it to a clearly better host (smart placement: the best measured path to tailnet.connects) instead of keeping it where it runs")
	wait := f.Duration("wait", 3*time.Minute, "how long to wait for ready")
	waitPublic := f.Duration("wait-public", 0, "also wait (up to this long) until the public URL answers from the public internet (twd.jsonc \"public\")")
	tenantF := tenantFlag(f)
	vars := varFlags{}
	f.Var(vars, "var", "override a wrangler var (KEY=VALUE, repeatable)")
	ao := addAppFlags(f) // wranglerapp.go: --env, --wrangler, --from-wrangler-out
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	a, err := loadAppWith(dir, *name, *ao)
	if err != nil {
		return err
	}
	if !*f.json {
		a.printWarnings(os.Stdout)
	}
	t0 := time.Now()
	res := deployResult{Workload: a.Name, Tenant: tn.Tailnet, TenantID: tn.TenantID, Timings: map[string]float64{}, Warnings: a.Warnings}
	ev := newEvents(*f.json, os.Stderr) // agentjson.go: NDJSON progress on stderr
	ev.emit("start", map[string]any{"workload": a.Name, "tenant": tn.Tailnet, "dir": a.Dir})
	ctx, cancel := context.WithTimeout(context.Background(), *wait+time.Minute)
	defer cancel()
	mc := s.mgmt()

	// Build first: the preflight is a dry-run deploy of the real bundle
	// (POST /v1/deployments?dryRun=true), so mgmt checks exactly the tags
	// and tailnet.connects it would mint and nothing is created.
	bs := time.Now()
	tb, jsBytes, err := a.build(vars)
	if err != nil {
		return err
	}
	res.BundleBytes = jsBytes
	res.Timings["build"] = ms(time.Since(bs))
	var pf mgmt.PreflightResult
	policyUnavailable := ""
	if err := mc.Do(ctx, "POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": tn.TenantID, "workload": a.Name, "bundle": tb}, &pf); err != nil {
		me, ok := mgmtapi.AsError(err)
		if !ok || me.Code != "policy_unavailable" {
			return err
		}
		// Keyless mode without a lifecycle credential (policy_file:read):
		// mgmt can't read the policy, so grants are not preflighted here;
		// a missing tagOwners entry still fails at mint (Tailscale checks).
		policyUnavailable = me.Message
		pf = mgmt.PreflightResult{OK: true, Unchecked: append([]string{}, a.Cfg.Tailnet.Connects...)}
	}
	res.Preflight = pf
	res.Timings["preflight"] = ms(time.Since(t0))
	ev.emit("preflight", map[string]any{"ok": pf.OK, "skipped": policyUnavailable != "", "preflight": pf})
	if !*f.json {
		if policyUnavailable != "" {
			fmt.Printf("› preflight  skipped: mgmt can't read the tailnet policy (%s)\n             add a lifecycle trust credential with scope policy_file:read (twd up --wif-lifecycle-client-id) to preflight grants; tags are still checked by Tailscale at mint\n", firstLine(policyUnavailable))
		} else {
			printPreflight(a, pf)
			printPublicPreflight(pf)
		}
	}
	if !pf.OK && !*allowMissing {
		return preflightFailure(pf)
	}

	if !*f.json {
		main := "dist/index.js"
		if a.Runtime == bundle.RuntimeNode {
			main = "dist/server.mjs (runtime node)"
		}
		fmt.Printf("✓ build      %s %.1f KB (%s) · bundle %.1f KB\n", main, float64(jsBytes)/1024, a.Opts.builder(), float64(len(tb))/1024)
	}
	// Containers (ADR-032 D3): build the Dockerfile images here and push them
	// to twd's registry; mgmt pins every image to a digest at deploy.
	var ilog io.Writer = os.Stdout
	if *f.json {
		lw := &lineWriter{ev: ev, fields: map[string]any{"source": "image-build"}}
		defer lw.Flush()
		ilog = lw
	}
	ev.emit("build", map[string]any{"bundleBytes": jsBytes, "runtime": a.Runtime})
	bctx, bcancel := context.WithTimeout(context.Background(), 60*time.Minute)
	builtImages, err := buildContainerImages(bctx, mc, a, tb, tn.TenantID, *platform, ilog)
	bcancel()
	if err != nil {
		return err
	}
	ds := time.Now()
	var dr mgmt.DeployResponse
	body := map[string]any{"tenantID": tn.TenantID, "workload": a.Name, "bundle": tb}
	if len(builtImages) > 0 {
		body["containerImages"] = builtImages
	}
	if *forceTakeover {
		body["forceTakeover"] = true
	}
	if *hostF != "" {
		body["host"] = *hostF
	}
	if *replace {
		body["replace"] = true
	}
	if err := mc.Do(ctx, "POST", "/v1/deployments", body, &dr); err != nil {
		return err
	}
	res.DeploymentID, res.Host, res.Placement = dr.DeploymentID, dr.Host, dr.Placement
	ev.emit("placed", map[string]any{"deploymentID": dr.DeploymentID, "host": dr.Host, "placement": dr.Placement})
	if !*f.json && dr.Placement != nil {
		fmt.Printf("✓ placed     %s\n", dr.Placement.Summary)
	}
	res.Timings["deploy_api"] = ms(time.Since(ds))
	if *noWait {
		res.Status = "assigned"
		emit(f, res, func() {
			fmt.Printf("✓ deploying  %s@%s (%s) · `twd status` to follow\n", a.Name, dr.Host, dr.DeploymentID)
		})
		return nil
	}
	var v mgmt.DeploymentView
	activeAt := time.Duration(0)
	lastStatus := ""
	for {
		if err := mc.Do(ctx, "GET", "/v1/deployments/"+dr.DeploymentID, nil, &v); err != nil {
			return err
		}
		if st := v.Status + "/" + v.Endpoint.Status; st != lastStatus {
			ev.emit("status", map[string]any{"deploymentID": dr.DeploymentID, "status": v.Status, "node": v.Endpoint.Status})
			lastStatus = st
		}
		if v.Status == "failed" {
			return fail("deploy_failed", "the host could not start "+a.Name, strings.Split(v.Error, "\n")...).withFix("twd logs " + a.Name)
		}
		if v.Status == "active" && activeAt == 0 {
			activeAt = time.Since(ds)
			res.Timings["active"] = ms(activeAt)
		}
		if v.Status == "active" && v.Endpoint.Status == "ready" && v.Endpoint.MagicDNS != "" {
			break
		}
		if v.Endpoint.Status == "failed" || v.Endpoint.Status == "killed" {
			return fail("bootstrap_failed", "the workload node for "+a.Name+" is "+v.Endpoint.Status).withFix("twd logs --component hostd")
		}
		if time.Since(ds) > *wait {
			return fail("identity_pending", fmt.Sprintf("%s not ready after %v (status %s, node %s)", a.Name, *wait, v.Status, v.Endpoint.Status)).withFix("twd status", "twd logs "+a.Name)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := waitReplicas(ctx, mc, dr, ds, *wait); err != nil {
		return err
	}
	res.Replicas = dr.Replicas
	res.Status, res.Endpoint, res.Public = v.Status, v.Endpoint, v.Public
	res.Timings["ready"] = ms(time.Since(ds))
	ev.emit("ready", map[string]any{"deploymentID": dr.DeploymentID, "host": v.Host, "magicDNS": v.Endpoint.MagicDNS, "url": "http://" + v.Endpoint.MagicDNS + "/"})
	if !*f.json {
		fmt.Printf("✓ deployed   %s@%s · node %s · http://%s/  (deploy → ready %.1fs)\n", a.Name, v.Host, v.Endpoint.Node, v.Endpoint.MagicDNS, res.Timings["ready"]/1000)
		if len(dr.Replicas) > 1 {
			parts := make([]string, 0, len(dr.Replicas))
			for _, rp := range dr.Replicas {
				parts = append(parts, rp.Workload+"@"+rp.Host)
			}
			fmt.Printf("✓ replicas   %d · %s\n", len(dr.Replicas), strings.Join(parts, ", "))
		}
		if sv := v.Endpoint.Service; sv != nil {
			fmt.Printf("✓ service    %s · http://%s/  (stable across redeploys and host moves; callers need a grant to %s)\n", sv.Name, sv.MagicDNS, sv.Name)
		}
		printPublic(v.Public)
	}
	if p := v.Public; *waitPublic > 0 && p != nil && p.Allowed && p.Exposed {
		last := ""
		d, err := waitPublicURL(ctx, p.URL, *waitPublic, func(s string) {
			if s != last {
				ev.emit("public_wait", map[string]any{"url": p.URL, "detail": firstLine(s)})
			}
			if !*f.json && s != last {
				fmt.Printf("  … waiting for %s from the public internet: %s\n", p.URL, firstLine(s))
				last = s
			}
		})
		if err != nil {
			return err
		}
		res.Timings["public"] = ms(d)
		ev.emit("public", map[string]any{"url": p.URL})
		if !*f.json {
			fmt.Printf("✓ public     %s answers from the public internet (%.0fs)\n", p.URL, d.Seconds())
		}
	}
	res.Timings["total"] = ms(time.Since(t0))
	if *f.json {
		emit(f, res, func() {})
	}
	return nil
}

func printPreflight(a *app, pf mgmt.PreflightResult) {
	if pf.Service != "" {
		if pf.ServiceUnapproved {
			fmt.Printf("› preflight: service %s … ✗ not in autoApprovers.services for the workload tags (control would never route it)\n    add to your tailnet policy:\n    %s\n", pf.Service, pf.ServiceSnippet)
		} else {
			fmt.Printf("✓ preflight  service %s auto-approved for the workload node\n", pf.Service)
		}
	}
	if len(a.Cfg.Tailnet.Connects) == 0 && len(pf.MissingTagOwners) == 0 {
		fmt.Printf("✓ preflight  no tailnet.connects declared · tagOwners ok for %s\n", strings.Join(a.srcTags(), ", "))
		return
	}
	for _, t := range pf.MissingTagOwners {
		fmt.Printf("› preflight: tagOwners %s … ✗ not in tagOwners (minting its nodes would fail)\n", t)
	}
	missing := map[string]bool{}
	for _, m := range pf.Missing {
		missing[m.Src+"→"+m.Dst] = true
		fmt.Printf("› preflight: %s → %s tcp:80 … ✗ no grant\n", m.Src, m.Dst)
	}
	for _, dst := range a.Cfg.Tailnet.Connects {
		if slices.Contains(pf.Unchecked, dst) {
			fmt.Printf("› preflight: → %s … ? a name, not a tag (not checked; name it by tag in tailnet.connects)\n", dst)
			continue
		}
		var ok []string
		for _, src := range a.srcTags() {
			if !missing[src+"→"+dst] {
				ok = append(ok, src)
			}
		}
		if len(ok) > 0 {
			fmt.Printf("✓ preflight  %s → %s granted\n", strings.Join(ok, ", "), dst)
		}
	}
	if len(pf.Missing) > 0 || len(pf.MissingTagOwners) > 0 {
		if len(pf.MissingTagOwners) > 0 {
			fmt.Printf("  add to your tailnet policy:\n    %s\n", pf.TagOwnersSnippet)
		}
		if len(pf.Missing) > 0 {
			fmt.Printf("  add to \"grants\" in your tailnet policy:\n")
			for _, m := range pf.Missing {
				fmt.Printf("    %s\n", m.Snippet)
			}
		}
	}
}
