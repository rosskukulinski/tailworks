package compat

// The published upstream-examples tables (issue #325). docs/COMPAT.md
// carries one generated block per Section, between
//
//	<!-- upstream-examples:<section>:begin -->
//	<!-- upstream-examples:<section>:end -->
//
// rendered from three things: the projects with that Section, the
// UpstreamNotRun list, and upstream-results.json, the outcomes of the last
// committed runs (`make e2e-compat-dev` and `make e2e-compat`).
// `make compat-upstream` takes new run files and rewrites the JSON and the
// blocks; `make compat-upstream-check` (part of `make lint`) fails when the
// blocks, the project list and the recorded results disagree, so a table
// row can't be edited by hand, and an example can't be added or re-pinned
// without running it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ResultsFile is the checked-in record of the last committed runs,
// relative to the repo root.
const ResultsFile = "e2e/compat/upstream-results.json"

// Recorded is one example's outcome in one run.
type Recorded struct {
	// Outcome is PASS or XFAIL (an expected failure, Project.Expect).
	Outcome string `json:"outcome"`
	// Source is repo/dir@commit as run: a re-pinned example must be rerun.
	Source string `json:"source"`
	Steps  int    `json:"steps,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RunRecord is one run of the suite.
type RunRecord struct {
	// Date is the day of the run (UTC).
	Date string `json:"date"`
	// Commit is the tailworks commit the run was built from.
	Commit   string              `json:"commit"`
	Platform string              `json:"platform"`
	Results  map[string]Recorded `json:"results"`
}

// Published is upstream-results.json.
type Published struct {
	// Dev is `make e2e-compat-dev`: each example under `tw dev`.
	Dev *RunRecord `json:"dev,omitempty"`
	// Deployed is `make e2e-compat`: tw deploy to a real host on a real
	// tailnet, called from a tailnet client.
	Deployed *RunRecord `json:"deployed,omitempty"`
}

// runFile is what the suite writes (writeRunFile in compat_test.go).
type runFile struct {
	Mode     string `json:"mode"`
	Date     string `json:"date"`
	Commit   string `json:"commit"`
	Platform string `json:"platform"`
	Projects []struct {
		ID      string   `json:"id"`
		Source  string   `json:"source"`
		Outcome string   `json:"outcome"`
		Detail  string   `json:"detail"`
		Steps   []string `json:"steps"`
	} `json:"projects"`
}

// LoadRunFile reads a results file of the suite and keeps the published
// examples' outcomes. mode is "dev" or "deployed". strict refuses a run
// with an unexpected outcome (FAIL, XPASS): that is for the nightly's
// summary to show, not for the repo to record.
func LoadRunFile(path, mode string, strict bool) (*RunRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f runFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Mode != mode {
		return nil, fmt.Errorf("%s: a %q run, want a %q run", path, f.Mode, mode)
	}
	published := map[string]*Project{}
	for _, p := range Projects() {
		if p.Section != "" {
			published[p.ID] = p
		}
	}
	rec := &RunRecord{Date: f.Date, Commit: f.Commit, Platform: f.Platform, Results: map[string]Recorded{}}
	for _, r := range f.Projects {
		p := published[r.ID]
		if p == nil {
			continue
		}
		out := Recorded{Outcome: r.Outcome, Source: r.Source}
		switch r.Outcome {
		case "PASS":
			out.Steps = len(r.Steps)
		case "XFAIL":
			// The reason is the project's Expect.Why; the detail is run
			// output (addresses, object IDs) and stays out of the repo.
		default:
			if strict {
				return nil, fmt.Errorf("%s: %s is %s (%s): classify it (fix it, or give the project an Expect) before publishing", path, r.ID, r.Outcome, tail(r.Detail, 300))
			}
		}
		rec.Results[r.ID] = out
	}
	return rec, nil
}

// Merge returns rec, or, when rec is a partial run (TW_COMPAT_ONLY: fewer
// examples than old has), old's results with rec's laid over them, under
// rec's date and commit.
func Merge(old, rec *RunRecord) *RunRecord {
	if old == nil || len(rec.Results) >= len(old.Results) {
		return rec
	}
	out := &RunRecord{Date: rec.Date, Commit: rec.Commit, Platform: rec.Platform, Results: map[string]Recorded{}}
	for id, r := range old.Results {
		out.Results[id] = r
	}
	for id, r := range rec.Results {
		out.Results[id] = r
	}
	return out
}

// LoadPublished reads upstream-results.json under root.
func LoadPublished(root string) (*Published, error) {
	b, err := os.ReadFile(root + "/" + ResultsFile)
	if err != nil {
		return nil, err
	}
	var p Published
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", ResultsFile, err)
	}
	return &p, nil
}

// Save writes upstream-results.json under root.
func (p *Published) Save(root string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(root+"/"+ResultsFile, append(b, '\n'), 0o644)
}

// source is how the suite names what it ran (Result.Source).
func (p *Project) source() string {
	return fmt.Sprintf("%s/%s@%s", p.Repo, p.Dir, p.Commit[:12])
}

// Verify checks that the recorded results cover the published projects as
// they are now: every one ran under tw dev at its current pin, and each
// outcome is the one its Expect says.
func (pub *Published) Verify() error {
	var errs []string
	if pub.Dev == nil {
		return fmt.Errorf("%s has no dev run: run `make e2e-compat-dev`, then `make compat-upstream`", ResultsFile)
	}
	ids := map[string]bool{}
	for _, p := range Projects() {
		if p.Section == "" {
			continue
		}
		ids[p.ID] = true
		for mode, run := range map[string]*RunRecord{"dev": pub.Dev, "deployed": pub.Deployed} {
			if run == nil {
				continue
			}
			want := "PASS"
			if p.ExpectIn(mode).Kind != ExpectPass {
				want = "XFAIL"
			}
			r, ok := run.Results[p.ID]
			switch {
			case !ok && mode == "dev":
				errs = append(errs, fmt.Sprintf("%s: no recorded tw dev result: run `make e2e-compat-dev`, then `make compat-upstream`", p.ID))
			case !ok:
			case r.Source != p.source():
				errs = append(errs, fmt.Sprintf("%s: the recorded %s result is for %s, the project is now %s: rerun it", p.ID, mode, r.Source, p.source()))
			case r.Outcome != want:
				errs = append(errs, fmt.Sprintf("%s: the recorded %s result is %s, the project expects %s: rerun it", p.ID, mode, r.Outcome, want))
			}
		}
	}
	for _, run := range []*RunRecord{pub.Dev, pub.Deployed} {
		if run == nil {
			continue
		}
		for id := range run.Results {
			if !ids[id] {
				errs = append(errs, fmt.Sprintf("%s: a recorded result for a project that is no longer published", id))
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("%s:\n  %s", ResultsFile, strings.Join(errs, "\n  "))
	}
	return nil
}

// Sections are the published sections, in COMPAT.md order, with the
// upstream project each one is about.
var Sections = []struct{ ID, Repo, Title string }{
	{SectionAgents, "cloudflare/agents", "Cloudflare's own Agents SDK examples"},
	{SectionVercel, "vercel/ai", "The Vercel AI SDK's examples"},
	{SectionMCP, "vercel/mcp-handler", "mcp-handler's examples"},
	{SectionPython, "cloudflare/python-workers-examples", "Cloudflare's Python Workers examples"},
}

func marker(section, end string) string {
	return "<!-- upstream-examples:" + section + ":" + end + " -->"
}

// UpdateDoc replaces every section's block in md (docs/COMPAT.md).
func UpdateDoc(md []byte, pub *Published) ([]byte, error) {
	for _, s := range Sections {
		begin, end := marker(s.ID, "begin"), marker(s.ID, "end")
		i, j := bytes.Index(md, []byte(begin)), bytes.Index(md, []byte(end))
		if i < 0 || j < i {
			return nil, fmt.Errorf("docs/COMPAT.md has no %s … %s block", begin, end)
		}
		var out bytes.Buffer
		out.Write(md[:i+len(begin)])
		out.WriteString("\n")
		out.WriteString(RenderSection(s.ID, pub))
		out.Write(md[j:])
		md = out.Bytes()
	}
	return md, nil
}

func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// changes is a project's Changes as one cell: numbered when there are
// several.
func changes(p *Project) string {
	if len(p.Changes) == 1 {
		return upperFirst(p.Changes[0])
	}
	var out []string
	for i, c := range p.Changes {
		out = append(out, fmt.Sprintf("(%d) %s.", i+1, upperFirst(c)))
	}
	return strings.Join(out, " ")
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// modeCell is one example's outcome in one run, as a table cell.
func modeCell(run *RunRecord, id string) string {
	if run == nil {
		return "not run"
	}
	r, ok := run.Results[id]
	switch {
	case !ok:
		return "not run"
	case r.Outcome == "PASS":
		return "pass"
	case r.Outcome == "XFAIL":
		return "fail"
	default: // only in a nightly summary: the repo never records these
		return "**" + r.Outcome + " (unexpected)**"
	}
}

func runLine(what string, run *RunRecord) string {
	if run == nil {
		return what + ": not run."
	}
	return fmt.Sprintf("%s: run %s on %s, tailworks `%s`.", what, run.Date, run.Platform, run.Commit[:min(12, len(run.Commit))])
}

// RenderSection is one section's generated block (without its markers).
func RenderSection(section string, pub *Published) string {
	var ps []*Project
	for _, p := range Projects() {
		if p.Section == section {
			ps = append(ps, p)
		}
	}
	var nr []NotRun
	for _, n := range UpstreamNotRun() {
		if n.Section == section {
			nr = append(nr, n)
		}
	}
	var repo, commit string
	for _, s := range Sections {
		if s.ID == section {
			repo = s.Repo
		}
	}
	for _, p := range ps {
		if p.Repo == repo {
			commit = p.Commit
		}
	}
	link := func(dir string) string {
		kind := "tree"
		if strings.HasSuffix(dir, ".ts") {
			kind = "blob"
		}
		return fmt.Sprintf("[`%s`](https://github.com/%s/%s/%s/%s)", strings.TrimPrefix(dir, "examples/"), repo, kind, commit, dir)
	}
	counts := map[string]int{}
	var rows strings.Builder
	for _, p := range ps {
		dev, dep := modeCell(pub.Dev, p.ID), modeCell(pub.Deployed, p.ID)
		// The result is the worse of the two modes; an example that runs
		// under tw dev and not deployed says both.
		passed, note := "pass", "—"
		if len(p.Changes) > 0 {
			passed, note = "pass with change", changes(p)
		}
		result, shown := passed, "**"+passed+"**"
		switch {
		case p.Expect.Kind != ExpectPass:
			result, shown, note = "fail", "**fail**", upperFirst(p.Expect.Why)
		case p.ExpectDev.Kind != ExpectPass:
			result, shown = "fail", "**fail** under `tw dev` ("+passed+" deployed)"
			note = "Under `tw dev`: " + p.ExpectDev.Why
		case p.ExpectDeployed.Kind != ExpectPass:
			result, shown = "fail", "**fail** deployed ("+passed+" under `tw dev`)"
			note = "Deployed: " + p.ExpectDeployed.Why
			if len(p.Changes) > 0 {
				note += ". Under `tw dev`: " + changes(p)
			}
		}
		counts[result]++
		dir := p.Dir
		if p.Repo == "vercel/mcp-handler" { // its examples are single files
			dir += "/route.ts"
		}
		fmt.Fprintf(&rows, "| %s | %s | %s | %s | %s | %s |\n", link(dir), cell(p.What), dev, dep, shown, cell(note))
	}
	n := 0
	for _, x := range nr {
		n += len(x.Dirs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- Generated by `make compat-upstream` from %s: don't edit this block by hand. -->\n\n", ResultsFile)
	if section == SectionPython { // python.go: the examples are the repo's top-level directories
		fmt.Fprintf(&b, "Upstream: [%s](https://github.com/%s/tree/%s), every top-level directory, at commit `%s`. ", repo, repo, commit, commit[:12])
	} else {
		fmt.Fprintf(&b, "Upstream: [%s](https://github.com/%s/tree/%s/examples) `examples/` at commit `%s`. ", repo, repo, commit, commit[:12])
	}
	b.WriteString(versions(section) + "\n\n")
	b.WriteString("- " + runLine("Under `tw dev` (`make e2e-compat-dev`, no tailnet and no credentials)", pub.Dev) + "\n")
	b.WriteString("- " + runLine("Deployed with `tw deploy` to a real host on a real tailnet and called from a tailnet client (`make e2e-compat`)", pub.Deployed) + "\n\n")
	fmt.Fprintf(&b, "%d examples: **%d pass, %d pass with a change, %d fail, %d not run.**%s A row's result is the worse of its two runs. \"Pass\" means the suite's probe passed: it does what the example's README or client does (the requests, the WebSocket and `@callable` calls, the MCP sessions), not everything the example can do.\n\n",
		len(ps)+n, counts["pass"], counts["pass with change"], counts["fail"], n, countNote(section))
	b.WriteString("| Example | What it is | `tw dev` | Deployed | Result | The change, or why it fails |\n|---|---|---|---|---|---|\n")
	b.WriteString(rows.String())
	if len(nr) > 0 {
		b.WriteString("\nNot run:\n\n| Why | Examples |\n|---|---|\n")
		for _, x := range nr {
			var names []string
			for _, d := range x.Dirs {
				names = append(names, "`"+d+"`")
			}
			fmt.Fprintf(&b, "| %s | %s |\n", cell(x.Why), strings.Join(names, ", "))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// countNote says how a section's examples were counted, when it isn't one
// directory each.
func countNote(section string) string {
	if section == SectionAgents {
		return " (The repo has 56 example directories. One of them, `next/`, holds 14 examples of its own, counted here one by one, with `next/harnesses` as one.)"
	}
	if section == SectionVercel {
		return " (`mcp` is one directory of MCP server and client script pairs; two of its servers run here, one row each, so it is counted twice.)"
	}
	return ""
}

// versions says what the section's examples were installed with.
func versions(section string) string {
	switch section {
	case SectionAgents:
		return "Versions: `agents` " + AgentsVersion + " (the examples depend on `\"agents\": \"*\"`, a workspace link; the suite installs the published version the repo carries at that commit, with `npm install`), `@modelcontextprotocol/server` " + MCPServerVersion + " and `@modelcontextprotocol/sdk` " + MCPSDKVersion + " (pinned by the examples themselves). No other versions were run, so none is known not to work."
	case SectionPython:
		return "Versions: the Pyodide bundles the pinned workerd runs (`internal/pyodide`); no package is vendored (`python_modules/`), so an example that needs one is not run. No other versions were run."
	case SectionVercel:
		return "Versions: `ai` " + AIVersion + " and `@ai-sdk/openai` " + AIOpenAIVersion + " (the examples depend on `workspace:*`; the suite installs the published versions the repo carries at that commit). No other versions were run."
	default:
		return "Versions: `mcp-handler` " + McpHandlerVersion + " (the repo's version at that commit), `@modelcontextprotocol/server` " + MCPServerVersion + ", `zod` " + zodVersion + ". No other versions were run."
	}
}
