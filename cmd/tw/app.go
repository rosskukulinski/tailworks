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

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
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
	tmpl := f.String("template", "hello", startingPoints(" | ")+" (tw init --list: every template)")
	list := f.Bool("list", false, "list every template: the starting points, then demos and patterns (templates.go)")
	name := f.String("name", "", "workload name (default: the directory name)")
	tags := f.String("tags", "", "use these existing tags for the workload node instead of a new per-app tag (comma-separated, e.g. tag:tw-app); see docs/tailworks-jsonc.md#tags-and-tw-init")
	var objectTags repeated
	f.Var(&objectTags, "object-tags", "use existing tags for the Durable Object classes with their own node: tag:a,tag:b for every class, or <Class>=tag:a,tag:b for one (repeatable)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if *list {
		return cmdInitList(f)
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw init needs a directory", "usage: tw "+usageOf("init"))
	}
	dir, _ := filepath.Abs(pos[0])
	ct, err := createTailworksDir()
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
	// --tags / --object-tags (inittags.go): check them against the template
	// before anything is written.
	tagSet, err := parseInitTagOverrides(*tags, objectTags)
	if err != nil {
		return err
	}
	if !tagSet.empty() {
		b, err := os.ReadFile(filepath.Join(src, "tailworks.jsonc"))
		if err != nil {
			return fail("bad_request", "template "+*tmpl+" has no tailworks.jsonc, so --tags and --object-tags have nothing to set")
		}
		if _, err := setTags(b, tagSet); err != nil {
			return err
		}
	}
	wl := *name
	if wl == "" {
		wl = filepath.Base(dir)
	}
	wl = workloadLabel(wl)
	if wl == "" {
		return fail("bad_request", "can't derive a workload name from "+dir).withFix("tw init <dir> --name <workload>")
	}
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		return err
	}
	// npm-style: templates ship "gitignore" (npm drops .gitignore files).
	if _, err := os.Stat(filepath.Join(dir, "gitignore")); err == nil {
		os.Rename(filepath.Join(dir, "gitignore"), filepath.Join(dir, ".gitignore"))
	}
	// Name the workload after the directory (tailworks.jsonc and wrangler.jsonc),
	// and the npm package too (its first "name" is the top-level one).
	// create-tailworks without tw does the same (lib/init.mjs); keep them in
	// step (TestCreateTailworksRenamesLikeInit).
	for _, fn := range []string{"package.json", "tailworks.jsonc", "wrangler.jsonc"} {
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
		if fn == "tailworks.jsonc" {
			if nb, _, err := appTags(b, wl); err == nil {
				b = nb
			}
		}
		if fn == "tailworks.jsonc" && !tagSet.empty() {
			if nb, err := setTags(b, tagSet); err == nil {
				b = nb
			}
		}
		os.WriteFile(p, b, 0o644)
	}
	// The template's workload tag (tailnet.tags, e.g. tag:tw-wl-hello)
	// follows the workload name too, so the policy snippet `tw deploy`
	// prints names this app, not the template it came from.
	if p := filepath.Join(dir, "tailworks.jsonc"); len(tagSet.workload) == 0 {
		if b, err := os.ReadFile(p); err == nil {
			if m := regexp.MustCompile(`"tags"\s*:\s*\[\s*"(tag:tw-wl-[A-Za-z0-9._-]+)"`).FindSubmatch(b); m != nil {
				if from, to := string(m[1]), "tag:tw-wl-"+wl; from != to {
					os.WriteFile(p, bytes.ReplaceAll(b, []byte(from), []byte(to)), 0o644)
				}
			}
		}
	}
	res := map[string]string{"dir": dir, "template": *tmpl, "workload": wl}
	emit(f, res, func() {
		fmt.Printf("✓ created %s from template %s (workload %q)\n", dir, *tmpl, wl)
		if tags := initTags(dir); len(tags) > 0 {
			fmt.Printf("  tags: %s (tw policy prints what to add to your tailnet policy for them)\n", strings.Join(tags, ", "))
		}
		printInitNext(pos[0], *tmpl) // templates.go
	})
	return nil
}

// ---- app + build ----

type app struct {
	Dir      string
	Name     string
	Cfg      *mgmt.TwConfig
	Wrangler map[string]any
	TwRaw    []byte
	// Runtime is "node" for the Node process tier (no wrangler config).
	Runtime string

	// Wrangler config (wranglerapp.go): the file read (wrangler.json,
	// .jsonc or .toml), the flags, and one line per ignored setting.
	WranglerFile string
	Opts         appOpts
	Warnings     []string

	// Pages is a Cloudflare Pages project (pages.go); nil for a Worker.
	Pages *pagesProject

	// WranglerName is the wrangler config's top-level "name", before
	// --env (appname.go compares it with tailworks.jsonc's).
	WranglerName string
}

func loadApp(dir, name string) (*app, error) { return loadAppWith(dir, name, appOpts{}) }

func loadAppWith(dir, name string, o appOpts) (*app, error) {
	dir, _ = filepath.Abs(dir)
	a := &app{Dir: dir, Opts: o}
	raw, twErr := os.ReadFile(filepath.Join(dir, "tailworks.jsonc"))
	var top struct {
		Name    string `json:"name"`
		Runtime string `json:"runtime"`
	}
	if twErr == nil {
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
			if dirExists(filepath.Join(dir, "functions")) { // pages.go
				return nil, fail("bad_request", "no wrangler config in "+dir+", but functions/ looks like a Cloudflare Pages project").
					withFix(`add a wrangler.jsonc: {"name": "<site>", "pages_build_output_dir": "./public"} (the folder your build writes)`)
			}
			if twErr != nil {
				return nil, fail("bad_request", "no tailworks.jsonc or wrangler config in "+dir).withFix("tw init <dir>   (tw init --list: the templates)")
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
	if twErr != nil {
		// A wrangler-only project: tailworks' defaults stand in for tailworks.jsonc.
		raw = defaultTwConfig(workloadLabel(a.Name))
		a.Warnings = append(a.Warnings, fmt.Sprintf("no tailworks.jsonc: using defaults (workload node tag:tw-wl-%s; Durable Object classes internal, no node of their own); add a tailworks.jsonc to change them", workloadLabel(a.Name)))
	}
	a.TwRaw = raw
	if err := checkTwSchema(raw); err != nil {
		return nil, err
	}
	var err error
	if a.Cfg, err = mgmt.ParseTwConfig(raw); err != nil {
		return nil, fail("bad_request", err.Error())
	}
	if wl := workloadLabel(a.Name); wl != a.Name {
		return nil, fail("bad_request", fmt.Sprintf("workload name %q is not a DNS label", a.Name)).withFix("tw deploy --name " + wl)
	}
	if err := a.defaultWorkloadTags(); err != nil { // defaulttags.go
		return nil, err
	}
	if err := a.defaultInternalClasses(); err != nil { // defaultobjects.go
		return nil, err
	}
	a.Warnings = append(a.Warnings, a.nameNotes(name, top.Name)...) // appname.go
	if a.Pages != nil {
		secrets, _ := typesSecrets(a)
		_, declared := genTypes(a, a.Dir, secrets)
		a.pagesEnvWarnings(declared) // pages.go
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
// twin of create-tailworks's build.mjs), into what hostd/gen expects
// (dist/index.js + tailworks.jsonc + wrangler.jsonc + tw-bundle.json), and packs it.
func (a *app) build(vars map[string]string) ([]byte, int, error) {
	if err := a.prebuild(); err != nil { // buildcmd.go: the wrangler config's build.command
		return nil, 0, err
	}
	if a.Runtime != bundle.RuntimeNode && isPythonApp(a.Wrangler) { // python.go: packed as it is, no bundler
		files, main, types, n, err := a.buildPython()
		if err != nil {
			return nil, 0, err
		}
		tb, err := a.packWorkerd(files, main, types, vars)
		return tb, n, err
	}
	ct, err := createTailworksDir()
	if err != nil {
		return nil, 0, err
	}
	out, err := os.MkdirTemp("", "tw-build-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(out)
	if a.Runtime == bundle.RuntimeNode {
		// The opt-in Node tier (M9) bundles with create-tailworks's build-node.mjs,
		// so it needs Node.js on the deploying machine; the workerd path does not.
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, 0, fail("node_required", `runtime "node" apps need Node.js on this machine to build`).
				withFix("install Node.js 24+ (e.g. https://nodejs.org) and re-run tw deploy")
		}
		return a.buildNode(node, ct, out, vars)
	}
	// In-process esbuild (Go API): no Node.js needed on this machine;
	// or wrangler's own bundler (--wrangler, --from-wrangler-out).
	var files map[string][]byte
	var types map[string]string
	var js int
	main := "dist/index.js"
	if a.Pages != nil && a.Pages.Err != nil {
		return nil, 0, a.Pages.Err // pages.go: a bad functions/ or _routes.json
	}
	if a.assetsOnly() {
		// An assets-only project (assets.go): no Worker to bundle.
		tb, err := a.packWorkerd(map[string][]byte{}, "", nil, vars)
		return tb, 0, err
	}
	if a.Opts.builder() == "wrangler" && a.Pages.hasCode() {
		_, err = a.pagesEntry(out) // pages.go: refuses --wrangler
		return nil, 0, err
	}
	switch a.builder() {
	case "wrangler":
		files, main, types, js, err = a.buildWrangler(out)
	case builderPrebuilt:
		files, main, types, js, err = a.buildPrebuilt() // nobundle.go
	default:
		files, types, js, err = a.buildEsbuild(out, nodePaths(ct))
	}
	if err != nil {
		return nil, 0, err
	}
	tb, err := a.packWorkerd(files, main, types, vars)
	return tb, js, err
}

// buildNode bundles a runtime "node" app with create-tailworks's build-node.mjs
// (dist/server.mjs, plus the Vercel Workflow bundles when workflows/ exists).
func (a *app) buildNode(node, ct, out string, vars map[string]string) ([]byte, int, error) {
	args := []string{filepath.Join(ct, "lib", "build-node.mjs"), a.Dir, filepath.Join(out, "app")}
	for k, v := range vars {
		args = append(args, "--var", k+"="+v)
	}
	cmd := exec.Command(node, args...)
	cmd.Dir = a.Dir
	cmd.Env = append(os.Environ(), "TW_NODE_PATH="+strings.Join(nodePaths(ct), ":"))
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, 0, withBundleFix(fail("build_failed", "build-node failed for "+a.Dir, strings.Split(strings.TrimSpace(string(b)), "\n")...).
			withFix("cd "+a.Dir+" && npm install"), string(b)) // sourcehints.go
	}
	files, err := bundle.ReadDir(filepath.Join(out, "app"), ".")
	if err != nil {
		return nil, 0, err
	}
	files["tailworks.jsonc"] = a.TwRaw // with the defaulted tailnet.tags (defaulttags.go)
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

	// Replicas (tailworks.jsonc "replicas" > 1): every copy and its host.
	Replicas []mgmt.ReplicaRef `json:"replicas,omitempty"`

	// Images: what each container image was pinned to (deploypins.go).
	Images []deployImage `json:"images,omitempty"`

	// Warnings: wrangler config tailworks ignores, one line per key.
	Warnings []string `json:"warnings,omitempty"`

	// Preview (--preview, ADR-048): the preview's own workload
	// name; Workload is the workload it previews.
	Preview string `json:"preview,omitempty"`

	// PolicyApplied: --apply-policy's result (policyapply.go).
	PolicyApplied *mgmt.PolicyApplyResult `json:"policyApplied,omitempty"`

	// Nodes: the tailnet nodes the app costs and the tenant's live count
	// before this deploy (nodebudget.go).
	Nodes *nodeBudget `json:"nodes,omitempty"`
}

func cmdDeploy(args []string) error {
	f := newFlags("deploy")
	name := f.String("name", "", "workload name (default: tailworks.jsonc name, then wrangler name, then the directory)")
	po := addPlacementFlags(f, "deploy") // placementflags.go
	allowMissing, noWait, wait := po.allowMissing, po.noWait, po.wait
	applyPolicy := f.Bool("apply-policy", false, "when the preflight finds tagOwners, grants, funnel nodeAttrs or Service auto-approval missing, write them to the tailnet policy first (as tw policy --apply; shows the lines and asks, --yes to skip). Without the flag tw deploy asks only on a terminal (not with --json or --yes); --apply-policy=false never asks or writes")
	yes := yesFlagBefore(f, "--apply-policy writes the policy")
	platform := f.String("platform", "", "containers: arches to build Dockerfile images for (linux/amd64,linux/arm64; default: the arches of the tenant pool's container hosts)")
	preview := f.Bool("preview", false, "deploy as a preview next to the workload (<workload>--preview-<alias>), leaving the workload as it is; tw promote makes it the workload")
	previewAlias := f.String("preview-alias", "", "--preview's alias, the <alias> in <workload>--preview-<alias> (implies --preview; default: the start of the bundle's SHA-256)")
	waitPublic := f.Duration("wait-public", 0, "also wait (up to this long) until the public URL answers from the public internet (tailworks.jsonc \"public\")")
	tenantF := tenantFlag(f)
	vars := varFlags{}
	f.Var(vars, "var", "override a wrangler var (KEY=VALUE, repeatable)")
	ao := addAppFlags(f) // wranglerapp.go: --env, --wrangler, --from-wrangler-out
	// --help lists these under Advanced (helpflags.go).
	f.group(advancedFlags,
		"platform",
		"wait-public",
		"wrangler",
		"from-wrangler-out",
	)
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
		// An OAuth client without policy_file:read: mgmt can't read the
		// policy, so grants are not preflighted here;
		// a missing tagOwners entry still fails at mint (Tailscale checks).
		policyUnavailable = me.Message
		pf = mgmt.PreflightResult{OK: true, Unchecked: append([]string{}, a.Cfg.Tailnet.Connects...)}
	}
	res.Preflight = pf
	res.Timings["preflight"] = ms(time.Since(t0))
	ev.emit("preflight", map[string]any{"ok": pf.OK, "skipped": policyUnavailable != "", "preflight": pf})
	if !*f.json {
		if policyUnavailable != "" {
			fmt.Printf("› preflight  skipped: mgmt can't read the tailnet policy (%s)\n             give the OAuth client the Policy File › Read scope to preflight grants; tags are still checked by Tailscale at mint\n", firstLine(policyUnavailable))
		} else {
			printPreflight(a, pf, policyModeOf(f, *applyPolicy, *yes) != policyPrintOnly)
			printPublicPreflight(pf)
		}
	}
	res.Nodes = s.nodeBudgetOf(ctx, tn, a.Cfg, *preview || *previewAlias != "") // nodebudget.go
	if !*f.json {
		printNodeBudget("✓ preflight  nodes: ", "! preflight  nodes: ", tn.Tailnet, res.Nodes)
	}
	if !pf.OK && !*allowMissing && policyModeOf(f, *applyPolicy, *yes) == policyAskFirst {
		if err := deployAskPolicy(ctx, mc, f, tn.TenantID, a.Name, tb, &pf, &res, ev); err != nil { // deploy_policy.go (#481)
			return err
		}
	}
	if !pf.OK && *applyPolicy {
		if err := deployApplyPolicy(ctx, mc, f, tn.TenantID, a.Name, tb, *yes, *allowMissing, &pf, &res, ev); err != nil { // deploy_policy.go
			return err
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
		fmt.Printf("✓ build      %s %.1f KB (%s) · bundle %.1f KB\n", main, float64(jsBytes)/1024, a.builder(), float64(len(tb))/1024)
	}
	// Containers (ADR-032 D3): build the Dockerfile images here and push them
	// to tailworks' registry; mgmt pins every image to a digest at deploy.
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
	po.apply(body)
	res.Preview = addPreview(body, a.Name, *preview, *previewAlias, tb) // preview.go
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
			fmt.Printf("✓ deploying  %s@%s (%s) · `tw status` to follow\n", a.Name, dr.Host, dr.DeploymentID)
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
			return fail("deploy_failed", "the host could not start "+a.Name, strings.Split(v.Error, "\n")...).withFix("tw logs " + a.Name)
		}
		if v.Status == "crashloop" { // it served, then kept exiting (hostd/restart.go)
			return fail("deploy_crashloop", a.Name+" started, then its workerd kept exiting; the host keeps restarting it with backoff", strings.Split(v.Error, "\n")...).withFix("tw logs " + a.Name)
		}
		if v.Status == "active" && activeAt == 0 {
			activeAt = time.Since(ds)
			res.Timings["active"] = ms(activeAt)
		}
		if v.Status == "active" && v.Endpoint.Status == "ready" && v.Endpoint.MagicDNS != "" {
			break
		}
		if v.Endpoint.Status == "failed" || v.Endpoint.Status == "killed" {
			return workloadNodeFailed(a.Name, v.Endpoint.Status) // workloadnode.go
		}
		if time.Since(ds) > *wait {
			return fail("identity_pending", fmt.Sprintf("%s not ready after %v (status %s, node %s)", a.Name, *wait, v.Status, v.Endpoint.Status)).withFix("tw status", "tw logs "+a.Name)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := waitReplicas(ctx, mc, dr, ds, *wait); err != nil {
		return err
	}
	res.Replicas = dr.Replicas
	res.Images = deployImages(v) // deploypins.go
	res.Status, res.Endpoint, res.Public = v.Status, v.Endpoint, v.Public
	res.Timings["ready"] = ms(time.Since(ds))
	wurl := workloadURL(a.TwRaw, v.Endpoint.MagicDNS) // tailnethttps.go
	ready := map[string]any{"deploymentID": dr.DeploymentID, "host": v.Host, "magicDNS": v.Endpoint.MagicDNS, "url": wurl}
	if len(res.Images) > 0 {
		ready["images"] = res.Images
	}
	ev.emit("ready", ready)
	if !*f.json {
		printDeployImages(os.Stdout, res.Images, builtImages)
		fmt.Printf("✓ deployed   %s@%s · node %s · %s  (deploy → ready %.1fs)\n", orName(res.Preview, a.Name), v.Host, v.Endpoint.Node, wurl, res.Timings["ready"]/1000)
		if res.Preview != "" {
			fmt.Print(previewNotice(a.Name, res.Preview, wurl))
		}
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
		printDeployNext(a.Name, wurl) // deploy_next.go
	}
	if p := v.Public; *waitPublic > 0 && p != nil && p.Allowed && p.Exposed {
		last := ""
		d, err := waitPublicURL(ctx, p.URL, *waitPublic, func(s string) {
			if s != last {
				ev.emit("public_wait", map[string]any{"url": p.URL, "detail": firstLine(s)})
			}
			if !*f.json && s != last {
				fmt.Printf("  … waiting for %s from the public internet: %s\n", p.URL, tsafe(firstLine(s)))
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

func printPreflight(a *app, pf mgmt.PreflightResult, applying bool) {
	for _, w := range pf.AccessWarnings { // tenant admins (docs/teams.md)
		fmt.Printf("! preflight  access: %s\n", w)
	}
	if pf.Service != "" {
		if pf.ServiceUnapproved {
			fmt.Printf("› preflight: service %s … ✗ not in autoApprovers.services for the workload tags (control would never route it)\n    add to your tailnet policy:\n", pf.Service)
			printPaste(snippetService(preflightPolicy(pf)), "    ") // policysnippet.go
		} else {
			fmt.Printf("✓ preflight  service %s auto-approved for the workload node\n", pf.Service)
		}
	}
	if len(a.Cfg.Tailnet.Connects) == 0 && len(pf.MissingTagOwners) == 0 && len(pf.UndefinedConnects) == 0 {
		fmt.Printf("✓ preflight  no tailnet.connects declared · tagOwners ok for %s\n", strings.Join(a.srcTags(), ", "))
		return
	}
	for _, t := range pf.MissingTagOwners {
		fmt.Printf("› preflight: tagOwners %s … ✗ not in tagOwners (minting its nodes would fail)\n", t)
	}
	for _, t := range pf.UndefinedConnects {
		fmt.Printf("› preflight: tagOwners %s … ✗ not in tagOwners (no app with this tag is deployed yet; a grant to it is rejected)\n", t)
	}
	missing := map[string]bool{}
	for _, m := range pf.Missing {
		missing[m.Src+"→"+m.Dst] = true
	}
	for _, g := range pf.MissingGrants() {
		fmt.Printf("› preflight: %s → %s %s … ✗ no grant\n", strings.Join(g.Srcs, ", "), g.Dst, g.IP)
	}
	for _, dst := range a.Cfg.Tailnet.Connects {
		if slices.Contains(pf.Unchecked, dst) {
			fmt.Printf("› preflight: → %s … ? a name, not a tag (not checked; name it by tag in tailnet.connects)\n", dst)
			continue
		}
		var ok []string
		target, _ := mgmt.SplitConnect(dst) // "tag:db:5432"
		for _, src := range a.srcTags() {
			if !missing[src+"→"+target] {
				ok = append(ok, src)
			}
		}
		if len(ok) > 0 {
			fmt.Printf("✓ preflight  %s → %s granted\n", strings.Join(ok, ", "), dst)
		}
	}
	if len(pf.Missing) > 0 || len(pf.MissingTagOwners) > 0 || len(pf.UndefinedConnects) > 0 {
		if c := snippetConnects(preflightPolicy(pf)); !c.Empty() { // policysnippet.go
			fmt.Printf("  add to your tailnet policy:\n")
			printPaste(c, "    ")
		}
		if len(pf.MissingTagOwners) > 0 && !applying { // --apply-policy: the plan that follows says what is written
			printCallerGrant(a.Cfg.Tailnet.Tags, a.Cfg.Tailnet.HTTPS, pf.Service) // callergrant.go
		}
	}
}
