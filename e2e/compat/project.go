// Package compat is the Cloudflare compatibility suite: unmodified example
// projects from Cloudflare's own repositories (cloudflare/templates, the
// create-cloudflare templates in cloudflare/workers-sdk, cloudflare/agents
// examples), each pinned to a commit, deployed with `tw deploy` to a real
// host on a real tailnet and called from a tailnet client the way their
// READMEs call them on Cloudflare.
//
// projects.go is the list; README.md says how to run it and add one. The
// run itself is compat_test.go (build tag e2ecompat, `make e2e-compat`).
package compat

import (
	"fmt"
	"regexp"
	"strings"
)

// Repos the suite takes projects from, with their licenses. Sources are
// fetched at run time (CacheDir), never vendored.
var Repos = map[string]string{
	"cloudflare/templates":               "MIT",
	"cloudflare/workers-sdk":             "Apache-2.0 OR MIT",
	"cloudflare/agents":                  "MIT",
	"cloudflare/ai":                      "MIT",
	"cloudflare/python-workers-examples": "Apache-2.0",
	"vercel/ai":                          "Apache-2.0",
	"vercel/mcp-handler":                 "Apache-2.0",
}

// Install says how a project's npm dependencies are installed.
type Install int

const (
	// InstallDefault: npm ci with a package-lock.json, else npm install.
	InstallDefault Install = iota
	// InstallUpgrade: npm install even with a lockfile (after Pins).
	InstallUpgrade
	// InstallNone: no npm step.
	InstallNone
)

// ExpectKind is what the suite expects of a project.
type ExpectKind int

const (
	// ExpectPass: it deploys and every probe passes.
	ExpectPass ExpectKind = iota
	// ExpectRefused: tw deploy must refuse it, with an error that names
	// the unsupported surface (Expect.Error).
	ExpectRefused
	// ExpectProbeFail: it deploys, and its probe must fail (a binding
	// tailworks doesn't provide). A pass is an XPASS: the suite fails so
	// COMPAT.md and this entry get updated.
	ExpectProbeFail
)

// Expect is a known gap, tied to its COMPAT.md row.
type Expect struct {
	Kind ExpectKind
	// Compat is the COMPAT.md anchor of the unsupported surface.
	Compat string
	// Error must appear in tw deploy's error output (ExpectRefused).
	Error string
	Why   string
}

// Project is one upstream example.
type Project struct {
	// ID is the workload name (and the prepared copy's directory).
	ID     string
	Repo   string
	Commit string
	// Dir is the project's directory in the repo.
	Dir string
	// What it is, in a few words (the README table).
	What string
	// Exercises lists COMPAT.md anchors this project covers.
	Exercises []string
	// C3: a create-cloudflare template; fill its <WORKER_NAME>,
	// <PACKAGE_NAME> and <COMPATIBILITY_DATE> placeholders as C3 does.
	C3 bool
	// Pins overrides package.json dependency versions (monorepo
	// examples' "workspace:*" or "*").
	Pins map[string]string
	// AddDeps adds dependencies (what a monorepo hoists for the example).
	AddDeps map[string]string
	Install Install
	// NpmArgs are extra npm ci / npm install flags.
	NpmArgs []string
	// Files are extra files written into the copy (path → content).
	// Only for what the project's README tells the user to create.
	Files map[string]string
	// Build runs after npm install, in the project directory (the
	// README's build step).
	Build [][]string
	// DeployDir: "" = the project directory; "redirect" = where the
	// Vite plugin's .wrangler/deploy/config.json points; else a path.
	DeployDir string
	// DeployArgs are extra tw deploy flags (e.g. --wrangler).
	DeployArgs []string
	// Secrets are put with tw secret before the deploy (the README's
	// `wrangler secret put`).
	Secrets map[string]string
	// Setup runs after the deploy, before the probe: tw commands
	// (--workload <ID> is appended), the README's post-deploy steps
	// (`wrangler d1 migrations apply`, …).
	Setup [][]string
	// NoOverlay: don't write tailworks.jsonc (only for ExpectRefused).
	NoOverlay bool
	// Workarounds lists every way this run differs from the plain
	// `npm install && npm run deploy` user flow, besides the tag overlay.
	Workarounds []string
	Expect      Expect
	// Probe calls the deployed workload. Nil only for ExpectRefused.
	Probe func(r *Run) error

	// Section is the COMPAT.md upstream-examples table this project is
	// published in (SectionAgents, SectionMCP, SectionVercel; issue #325).
	// "" = not published there. `make e2e-compat-dev` runs these.
	Section string
	// Changes lists what the run changes in the example itself (its
	// config, an added entry file, a stubbed model). With any, a pass is
	// published as "pass with change". Workarounds are install and deploy
	// mechanics; Changes are differences a user would have to make too.
	Changes []string
	// OriginVar names a wrangler `vars` entry the example's README says to
	// set to the deployed URL. The run sets it to <origin>/mcp
	// (setOriginVar): under tw dev before the start, deployed after a first
	// deploy has shown the origin, followed by a second deploy.
	OriginVar string
	// ExpectDeployed, when its Kind isn't ExpectPass, replaces Expect in
	// the deployed run: the example behaves differently on a real host
	// than under tw dev.
	ExpectDeployed Expect

	// Companions are projects deployed before this one, each under its own
	// ID: the Workers its `services` bindings name, in an example made of
	// several Workers. They are prepared like a project, never probed or
	// published on their own. `tw dev` runs one Worker, so the dev run
	// doesn't start them (set ExpectDev).
	Companions []*Project

	// ExpectDev, when its Kind isn't ExpectPass, replaces Expect in the
	// `tw dev` run (a multi-Worker example: tw dev has no binding to
	// another workload).
	ExpectDev Expect

	// TailnetHTTPS sets tailworks.jsonc tailnet.https in the overlay
	// (#360): the deployed run calls the workload at
	// https://<magicDNS> and gives an OriginVar that origin. tw dev
	// ignores it (loopback http://). List it in Changes too.
	TailnetHTTPS bool
}

// ExpectIn is what the suite expects of p in a run mode ("dev" or
// "deployed").
func (p *Project) ExpectIn(mode string) Expect {
	if mode == "deployed" && p.ExpectDeployed.Kind != ExpectPass {
		return p.ExpectDeployed
	}
	if mode == "dev" && p.ExpectDev.Kind != ExpectPass {
		return p.ExpectDev
	}
	return p.Expect
}

var (
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Validate checks the list: unique IDs that are valid workload names,
// pinned commits, known repos, a probe for everything that deploys, and
// that every Exercises / Expect.Compat anchor is in anchors (COMPAT.md's).
func Validate(ps []*Project, anchors map[string]bool) error {
	var errs []string
	seen := map[string]bool{}
	for _, p := range ps {
		bad := func(f string, a ...any) { errs = append(errs, p.ID+": "+fmt.Sprintf(f, a...)) }
		if !idRe.MatchString(p.ID) {
			bad("id is not a valid workload name")
		}
		if seen[p.ID] {
			bad("duplicate id")
		}
		seen[p.ID] = true
		if _, ok := Repos[p.Repo]; !ok {
			bad("repo %q is not in Repos", p.Repo)
		}
		if !commitRe.MatchString(p.Commit) {
			bad("commit %q is not a full SHA", p.Commit)
		}
		if p.What == "" || len(p.Exercises) == 0 {
			bad("needs What and Exercises")
		}
		for _, a := range append(append([]string{}, p.Exercises...), p.Expect.Compat, p.ExpectDeployed.Compat, p.ExpectDev.Compat) {
			if a != "" && anchors != nil && !anchors[a] {
				bad("COMPAT.md has no anchor %q", a)
			}
		}
		switch p.Expect.Kind {
		case ExpectPass:
			if p.Probe == nil {
				bad("no probe")
			}
		case ExpectRefused:
			if p.Expect.Error == "" || p.Expect.Compat == "" {
				bad("an expected refusal needs Expect.Error and Expect.Compat")
			}
		case ExpectProbeFail:
			if p.Probe == nil || p.Expect.Compat == "" {
				bad("an expected probe failure needs a probe and Expect.Compat")
			}
		}
		if p.NoOverlay && p.Expect.Kind != ExpectRefused {
			bad("NoOverlay is only for expected refusals")
		}
	}
	if n := countPass(ps); n < 20 {
		errs = append(errs, fmt.Sprintf("only %d projects are expected to pass; the suite promises at least 20", n))
	}
	if len(errs) > 0 {
		return fmt.Errorf("compat projects:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func countPass(ps []*Project) int {
	n := 0
	for _, p := range ps {
		if p.Expect.Kind == ExpectPass {
			n++
		}
	}
	return n
}
