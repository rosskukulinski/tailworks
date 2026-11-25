// Package agent ships tailworks' skills for coding agents (Claude Code, Codex,
// Cursor, OpenCode, Gemini CLI) and installs them (`tw agent setup`,
// issue #25, docs/agents.md).
//
// The skills are Markdown with front matter in skills/<name>/SKILL.md,
// embedded in the tailworks binary so they are versioned with the CLI. Plan
// works out what setup or remove would write for each detected agent, in
// that agent's format:
//
//   - Claude Code: one skill directory each, <root>/.claude/skills/<name>/SKILL.md;
//   - Cursor: one rule each, <root>/.cursor/rules/<name>.mdc (project only:
//     Cursor keeps user rules in its settings, not in files);
//   - Codex and OpenCode (AGENTS.md) and Gemini CLI (GEMINI.md): one
//     managed block in the instructions file: the core skill plus an index
//     of the others (`tw agent show <name>` prints one).
//
// Everything tailworks writes carries a marker, so updates and `tw agent remove`
// touch only tailworks' own files and blocks, never the user's.
package agent

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed skills
var skillsFS embed.FS

// Skill is one skill: its front matter and Markdown body.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Body is the Markdown after the front matter.
	Body string `json:"body,omitempty"`
}

// CoreSkill is the entry point every agent gets in full.
const CoreSkill = "tw"

// Skills returns every embedded skill, sorted by name with the core skill
// first.
func Skills() []Skill {
	var out []Skill
	ents, _ := fs.ReadDir(skillsFS, "skills")
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := skillsFS.ReadFile(path.Join("skills", e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		s, err := ParseSkill(b)
		if err != nil {
			panic("agent: skills/" + e.Name() + ": " + err.Error())
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Name == CoreSkill) != (out[j].Name == CoreSkill) {
			return out[i].Name == CoreSkill
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Source returns a skill's SKILL.md as shipped, or false.
func Source(name string) ([]byte, bool) {
	b, err := skillsFS.ReadFile(path.Join("skills", name, "SKILL.md"))
	return b, err == nil
}

// Find returns the named skill.
func Find(name string) (Skill, bool) {
	for _, s := range Skills() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// ParseSkill reads a SKILL.md: "---", name: and description: lines, "---",
// then the body.
func ParseSkill(b []byte) (Skill, error) {
	s := string(b)
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return Skill{}, fmt.Errorf("no front matter")
	}
	fm, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Skill{}, fmt.Errorf("unterminated front matter")
	}
	var sk Skill
	for _, l := range strings.Split(fm, "\n") {
		k, v, _ := strings.Cut(l, ":")
		switch strings.TrimSpace(k) {
		case "name":
			sk.Name = strings.TrimSpace(v)
		case "description":
			sk.Description = strings.TrimSpace(v)
		}
	}
	if sk.Name == "" || sk.Description == "" {
		return Skill{}, fmt.Errorf("front matter needs name and description")
	}
	sk.Body = strings.TrimLeft(body, "\n")
	return sk, nil
}

// ---- agents ----

// Format is how an agent reads instructions.
type Format string

const (
	// FormatClaudeSkill is a skill directory per skill (SKILL.md).
	FormatClaudeSkill Format = "claude-skill"
	// FormatCursorRule is an .mdc rule file per skill.
	FormatCursorRule Format = "cursor-rule"
	// FormatBlock is one managed block in a shared Markdown file.
	FormatBlock Format = "block"
)

// Agent is a coding agent tailworks knows how to teach.
type Agent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Dirs (relative to home) and Bins (on PATH): any present means the
	// agent is installed.
	Dirs []string `json:"-"`
	Bins []string `json:"-"`
	// Where it reads instructions, relative to home (user scope) or to the
	// project directory. "" = not supported in that scope.
	Format  Format `json:"format"`
	User    string `json:"-"`
	Project string `json:"-"`
	// NoUser explains a missing user scope.
	NoUser string `json:"-"`
}

// Agents is every agent tailworks knows, in display order.
var Agents = []Agent{
	{ID: "claude", Name: "Claude Code", Dirs: []string{".claude"}, Bins: []string{"claude"},
		Format: FormatClaudeSkill, User: ".claude/skills", Project: ".claude/skills"},
	{ID: "codex", Name: "Codex", Dirs: []string{".codex"}, Bins: []string{"codex"},
		Format: FormatBlock, User: ".codex/AGENTS.md", Project: "AGENTS.md"},
	{ID: "cursor", Name: "Cursor", Dirs: []string{".cursor"}, Bins: []string{"cursor", "cursor-agent"},
		Format: FormatCursorRule, Project: ".cursor/rules",
		NoUser: "Cursor keeps user rules in its settings, not in files: use --scope project"},
	{ID: "gemini", Name: "Gemini CLI", Dirs: []string{".gemini"}, Bins: []string{"gemini"},
		Format: FormatBlock, User: ".gemini/GEMINI.md", Project: "GEMINI.md"},
	{ID: "opencode", Name: "OpenCode", Dirs: []string{".config/opencode"}, Bins: []string{"opencode"},
		Format: FormatBlock, User: ".config/opencode/AGENTS.md", Project: "AGENTS.md"},
}

// AgentByID returns the agent with this ID.
func AgentByID(id string) (Agent, bool) {
	for _, a := range Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Env is the machine Plan looks at.
type Env struct {
	Home string
	// LookPath finds a binary (exec.LookPath); nil = binaries aren't checked.
	LookPath func(string) (string, error)
}

// Detection says whether an agent looks installed, and why.
type Detection struct {
	Agent
	Detected bool     `json:"detected"`
	Found    []string `json:"found,omitempty"` // "~/.claude", "claude on PATH"
}

// Detect checks every known agent.
func Detect(env Env) []Detection {
	var out []Detection
	for _, a := range Agents {
		d := Detection{Agent: a}
		for _, dir := range a.Dirs {
			if st, err := os.Stat(filepath.Join(env.Home, dir)); err == nil && st.IsDir() {
				d.Found = append(d.Found, "~/"+dir)
			}
		}
		if env.LookPath != nil {
			for _, b := range a.Bins {
				if _, err := env.LookPath(b); err == nil {
					d.Found = append(d.Found, b+" on PATH")
				}
			}
		}
		d.Detected = len(d.Found) > 0
		out = append(out, d)
	}
	return out
}

// ---- rendering ----

// marker identifies tw-written content; version is the tailworks that wrote it.
func marker(version string) string {
	return "<!-- tw agent skills " + version + ": written by `tw agent setup`, removed by `tw agent remove`. Edit the source in the tailworks repo (agent/skills), not here. -->"
}

const markerPrefix = "<!-- tw agent skills "

// Block delimiters in shared instruction files.
const (
	blockBegin = "<!-- BEGIN tw agent skills -->"
	blockEnd   = "<!-- END tw agent skills -->"
)

func renderClaude(s Skill, version string) string {
	return "---\nname: " + s.Name + "\ndescription: " + s.Description + "\n---\n\n" + marker(version) + "\n\n" + s.Body
}

func renderCursor(s Skill, version string) string {
	return "---\ndescription: " + s.Description + "\nglobs:\nalwaysApply: false\n---\n\n" + marker(version) + "\n\n" + s.Body
}

// renderBlock is the managed block for AGENTS.md / GEMINI.md: the core
// skill, then an index of the others.
func renderBlock(skills []Skill, version string) string {
	var b strings.Builder
	b.WriteString(blockBegin + "\n" + marker(version) + "\n\n")
	for _, s := range skills {
		if s.Name == CoreSkill {
			// Demote the core skill's headings one level under the file's own.
			for _, l := range strings.Split(strings.TrimRight(s.Body, "\n"), "\n") {
				if strings.HasPrefix(l, "#") {
					l = "#" + l
				}
				b.WriteString(l + "\n")
			}
		}
	}
	b.WriteString("\n### tw skills\n\nPrint a skill with `tw agent show <skill>` before you start that task:\n\n")
	for _, s := range skills {
		if s.Name != CoreSkill {
			fmt.Fprintf(&b, "- `%s`: %s\n", s.Name, s.Description)
		}
	}
	b.WriteString("\n" + blockEnd + "\n")
	return b.String()
}

// ---- plan ----

// Scope is where skills go.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// Action is what happens to one path.
type Action string

const (
	Create    Action = "create"
	Update    Action = "update"
	Unchanged Action = "unchanged"
	Remove    Action = "remove"
	// Skip: a file tailworks didn't write is in the way; it is left alone.
	Skip Action = "skip"
)

// Change is one file (or managed block) to write or delete.
type Change struct {
	Action Action `json:"action"`
	Path   string `json:"path"`
	// Kind is "file" or "block" (a managed block in a shared file).
	Kind   string   `json:"kind"`
	Agents []string `json:"agents"`
	Skill  string   `json:"skill,omitempty"`
	Note   string   `json:"note,omitempty"`

	content []byte // the whole file after the change; nil = delete it
}

// Plan is what setup or remove would do.
type Plan struct {
	Scope   Scope    `json:"scope"`
	Version string   `json:"version"`
	Root    string   `json:"root"`
	Changes []Change `json:"changes"`
	// Skipped are selected agents with nothing to do in this scope.
	Skipped []string `json:"skipped,omitempty"`
}

// Pending is the number of changes that would write or delete something.
func (p *Plan) Pending() int {
	n := 0
	for _, c := range p.Changes {
		if c.Action == Create || c.Action == Update || c.Action == Remove {
			n++
		}
	}
	return n
}

// Options select what to plan.
type Options struct {
	Scope Scope
	// Dir is the project directory (ScopeProject).
	Dir     string
	Agents  []Agent
	Version string
	// Remove plans `tw agent remove` instead of setup.
	Remove bool

	// Refresh only updates what tailworks already wrote (refresh.go).
	Refresh bool
}

// target is one place to write, shared by the agents that read it.
type target struct {
	path   string
	format Format
	agents []string
}

// MakePlan works out the changes for opts on env.
func MakePlan(env Env, opts Options) (*Plan, error) {
	root := env.Home
	if opts.Scope == ScopeProject {
		root = opts.Dir
	}
	p := &Plan{Scope: opts.Scope, Version: opts.Version, Root: root, Changes: []Change{}}
	var targets []*target
	byPath := map[string]*target{}
	for _, a := range opts.Agents {
		rel := a.User
		if opts.Scope == ScopeProject {
			rel = a.Project
		}
		if rel == "" {
			p.Skipped = append(p.Skipped, a.Name+": "+a.NoUser)
			continue
		}
		pth := filepath.Join(root, filepath.FromSlash(rel))
		if t, ok := byPath[pth]; ok {
			t.agents = append(t.agents, a.Name)
			continue
		}
		t := &target{path: pth, format: a.Format, agents: []string{a.Name}}
		byPath[pth] = t
		targets = append(targets, t)
	}
	skills := Skills()
	for _, t := range targets {
		var cs []Change
		var err error
		switch t.format {
		case FormatClaudeSkill:
			cs, err = planFiles(t, skills, opts, func(s Skill) string { return filepath.Join(t.path, s.Name, "SKILL.md") }, renderClaude)
		case FormatCursorRule:
			cs, err = planFiles(t, skills, opts, func(s Skill) string { return filepath.Join(t.path, s.Name+".mdc") }, renderCursor)
		case FormatBlock:
			var c Change
			c, err = planBlock(t, skills, opts)
			cs = []Change{c}
		}
		if err != nil {
			return nil, err
		}
		p.Changes = append(p.Changes, refreshOnly(cs, opts)...)
	}
	return p, nil
}

func isOurs(b []byte) bool { return bytes.Contains(b, []byte(markerPrefix)) }

// planFiles plans one file per skill, plus removal of tw-written files
// for skills this version no longer ships.
func planFiles(t *target, skills []Skill, opts Options, at func(Skill) string, render func(Skill, string) string) ([]Change, error) {
	var out []Change
	want := map[string]bool{}
	for _, s := range skills {
		p := at(s)
		want[p] = true
		c := Change{Path: p, Kind: "file", Agents: t.agents, Skill: s.Name}
		old, err := os.ReadFile(p)
		exists := err == nil
		ours := exists && isOurs(old)
		switch {
		case opts.Remove && ours:
			c.Action = Remove
		case opts.Remove:
			continue
		case exists && !ours:
			c.Action, c.Note = Skip, "exists and wasn't written by tailworks; left alone"
		default:
			c.content = []byte(render(s, opts.Version))
			switch {
			case !exists:
				c.Action = Create
			case bytes.Equal(old, c.content):
				c.Action = Unchanged
			default:
				c.Action = Update
			}
		}
		out = append(out, c)
	}
	// Stale: tw-written skills of another version that this one doesn't ship.
	matches, _ := filepath.Glob(at(Skill{Name: "tw*"}))
	sort.Strings(matches)
	for _, m := range matches {
		if want[m] {
			continue
		}
		if b, err := os.ReadFile(m); err == nil && isOurs(b) {
			out = append(out, Change{Action: Remove, Path: m, Kind: "file", Agents: t.agents, Note: "no longer shipped"})
		}
	}
	return out, nil
}

// planBlock plans the managed block in a shared instructions file.
func planBlock(t *target, skills []Skill, opts Options) (Change, error) {
	c := Change{Path: t.path, Kind: "block", Agents: t.agents}
	old, err := os.ReadFile(t.path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return c, err
	}
	before, _, after, has, err := splitBlock(string(old))
	if err != nil {
		return c, fmt.Errorf("%s: %w", t.path, err)
	}
	if opts.Remove {
		if !has {
			c.Action = Unchanged
			return c, nil
		}
		c.Action = Remove
		rest := joinAround(before, after)
		if strings.TrimSpace(rest) != "" {
			c.content = []byte(rest) // the file stays, without the block
			c.Note = "removes the tailworks block; the rest of the file stays"
		}
		return c, nil
	}
	if opts.Refresh && !has {
		c.Action = Skip // refresh.go: no tailworks block to update
		return c, nil
	}
	block := renderBlock(skills, opts.Version)
	var next string
	switch {
	case has:
		next = before + block + after
	case strings.TrimSpace(string(old)) == "":
		next = block
	default:
		next = strings.TrimRight(string(old), "\n") + "\n\n" + block
		c.Note = "appends a tailworks block; the rest of the file stays"
	}
	c.content = []byte(next)
	switch {
	case !exists:
		c.Action = Create
	case next == string(old):
		c.Action = Unchanged
	default:
		c.Action = Update
	}
	return c, nil
}

// splitBlock finds the managed block: before it, the block itself
// (delimiters and trailing newline included) and after it.
func splitBlock(s string) (before, block, after string, ok bool, err error) {
	i := strings.Index(s, blockBegin)
	if i < 0 {
		return s, "", "", false, nil
	}
	j := strings.Index(s[i:], blockEnd)
	if j < 0 {
		return "", "", "", false, fmt.Errorf("a %q without %q: fix or delete the tailworks block by hand", blockBegin, blockEnd)
	}
	end := i + j + len(blockEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	return s[:i], s[i:end], s[end:], true, nil
}

// joinAround joins what was before and after a removed block, dropping the
// blank line setup added in front of it.
func joinAround(before, after string) string {
	before = strings.TrimRight(before, "\n")
	if before != "" {
		before += "\n"
	}
	if after != "" && before != "" {
		return before + "\n" + strings.TrimLeft(after, "\n")
	}
	return before + strings.TrimLeft(after, "\n")
}

// Apply carries out the plan's changes.
func Apply(p *Plan) error {
	for _, c := range p.Changes {
		switch c.Action {
		case Create, Update:
			if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(c.Path, c.content, 0o644); err != nil {
				return err
			}
		case Remove:
			if c.content != nil {
				if err := os.WriteFile(c.Path, c.content, 0o644); err != nil {
					return err
				}
				continue
			}
			if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			// A Claude skill directory tailworks made: remove it once empty.
			if filepath.Base(c.Path) == "SKILL.md" {
				os.Remove(filepath.Dir(c.Path))
			}
		}
	}
	return nil
}
