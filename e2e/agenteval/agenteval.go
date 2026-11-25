// Package agenteval is the scripted agent loop of issue #25: no LLM, it
// replays the commands tailworks' skills (agent/skills) tell a coding agent to
// run, block by block, against a real tailworks (the hermetic harness in
// cmd/tw TestAgentEval: tw up on testcontrol and a fake Tailscale API),
// and checks every --json output against schema/cli.schema.json. A skill
// that names a command, flag or output field that's gone fails it.
package agenteval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	twagent "github.com/rosskukulinski/tailworks/agent"
	repodocs "github.com/rosskukulinski/tailworks/docs"
	"github.com/rosskukulinski/tailworks/schema"
)

// Order is the order the eval replays the skills' eval blocks in: an
// agent's loop from an empty directory to a stopped workload.
var Order = []string{"scaffold", "dev", "deploy", "inspect", "logs", "teardown"}

// Steps returns every skill's eval steps by name.
func Steps() (map[string]twagent.EvalStep, error) {
	out := map[string]twagent.EvalStep{}
	for _, s := range twagent.Skills() {
		_, steps, err := twagent.Commands(s)
		if err != nil {
			return nil, err
		}
		for _, st := range steps {
			if _, dup := out[st.Name]; dup {
				return nil, fmt.Errorf("eval step %q twice", st.Name)
			}
			out[st.Name] = st
		}
	}
	return out, nil
}

// Run is one tailworks invocation and its outcome.
type Run struct {
	Step   string        `json:"step,omitempty"`
	Ref    string        `json:"ref,omitempty"` // skill:line
	Args   []string      `json:"args"`
	Dir    string        `json:"dir"`
	Stdout string        `json:"stdout"`
	Stderr string        `json:"stderr"`
	Exit   int           `json:"exit"`
	Dur    time.Duration `json:"durNs"`
}

// OK reports a zero exit.
func (r Run) OK() bool { return r.Exit == 0 }

// Agent is the scripted agent: a working directory (cd moves it) and the
// tailworks binary, state dir and environment it runs with.
type Agent struct {
	T        testing.TB
	Bin      string // the tw binary
	StateDir string
	Dir      string
	Env      []string
	// Stdin, if set, is fed to the next Exec (then cleared).
	Stdin string
	Log   []Run
}

// Exec runs `tw <args>` in a.Dir with --state-dir.
func (a *Agent) Exec(args ...string) Run {
	full := append([]string{"--state-dir", a.StateDir}, args...)
	cmd := exec.Command(a.Bin, full...)
	cmd.Dir = a.Dir
	cmd.Env = append(os.Environ(), a.Env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if a.Stdin != "" {
		cmd.Stdin = strings.NewReader(a.Stdin)
		a.Stdin = ""
	}
	st := time.Now()
	err := cmd.Run()
	r := Run{Args: args, Dir: a.Dir, Stdout: so.String(), Stderr: se.String(), Dur: time.Since(st)}
	if ee, ok := err.(*exec.ExitError); ok {
		r.Exit = ee.ExitCode()
	} else if err != nil {
		a.T.Fatalf("tw %s: %v", strings.Join(args, " "), err)
	}
	a.Log = append(a.Log, r)
	return r
}

// Replay runs one eval step's commands as written; `cd` moves the agent.
func (a *Agent) Replay(step twagent.EvalStep) []Run {
	var out []Run
	for _, c := range step.Commands {
		if c.Cd != "" {
			d := c.Cd
			if !filepath.IsAbs(d) {
				d = filepath.Join(a.Dir, d)
			}
			if st, err := os.Stat(d); err != nil || !st.IsDir() {
				a.T.Fatalf("%s:%d (step %s): cd %s: not a directory", c.Skill, c.Line, step.Name, c.Cd)
			}
			a.Dir = d
			continue
		}
		r := a.Exec(c.Args...)
		r.Step, r.Ref = step.Name, fmt.Sprintf("%s:%d", c.Skill, c.Line)
		a.Log[len(a.Log)-1] = r
		out = append(out, r)
	}
	return out
}

// ---- output contract (docs/cli-json.md) ----

// Doc decodes stdout as exactly one JSON document.
func Doc(r Run) (any, error) {
	dec := json.NewDecoder(strings.NewReader(r.Stdout))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("stdout is not JSON: %v\n%s", err, r.Stdout)
	}
	var extra any
	if dec.Decode(&extra) == nil {
		return nil, fmt.Errorf("stdout has more than one JSON document:\n%s", r.Stdout)
	}
	return v, nil
}

// Lines decodes NDJSON: every non-empty line one JSON object.
func Lines(s string) ([]map[string]any, error) {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			return nil, fmt.Errorf("line %d is not a JSON object: %v\n%s", n, err, l)
		}
		out = append(out, m)
	}
	return out, nil
}

// CheckDoc checks v against the schema's $defs/<def>.
func CheckDoc(def string, v any) error {
	ps, err := schema.CheckCLI(def, v)
	if err != nil {
		return err
	}
	if len(ps) > 0 {
		var b strings.Builder
		for _, p := range ps {
			b.WriteString("\n  " + p.String())
		}
		js, _ := json.MarshalIndent(v, "", "  ")
		return fmt.Errorf("%s does not match schema/cli.schema.json $defs/%q:%s\n%s", def, def, b.String(), trim(string(js), 3000))
	}
	return nil
}

// CheckResult checks a successful run: one JSON document on stdout that
// matches $defs/<def>.
func CheckResult(def string, r Run) error {
	if !r.OK() {
		return fmt.Errorf("tw %s exited %d\n%s", strings.Join(r.Args, " "), r.Exit, r.Stderr)
	}
	v, err := Doc(r)
	if err != nil {
		return err
	}
	return CheckDoc(def, v)
}

// CheckStream checks every NDJSON line of s against $defs/<def>.
func CheckStream(def, s string) ([]map[string]any, error) {
	ls, err := Lines(s)
	if err != nil {
		return nil, err
	}
	for _, l := range ls {
		if err := CheckDoc(def, l); err != nil {
			return nil, err
		}
	}
	return ls, nil
}

// CheckLogs checks `tw logs --json` output: request records
// ($defs/logs.record, they have "kind") or raw lines ($defs/logs.line).
func CheckLogs(s string) ([]map[string]any, error) {
	ls, err := Lines(s)
	if err != nil {
		return nil, err
	}
	for _, l := range ls {
		def := "logs.line"
		if _, ok := l["kind"]; ok {
			def = "logs.record"
		}
		if err := CheckDoc(def, l); err != nil {
			return nil, err
		}
	}
	return ls, nil
}

var codeHeading = regexp.MustCompile(`(?m)^### ([a-z0-9_]+)$`)

// KnownCodes are the error codes docs/errors.md documents.
func KnownCodes() map[string]bool {
	md, _ := repodocs.Read("errors.md")
	out := map[string]bool{}
	for _, m := range codeHeading.FindAllStringSubmatch(md, -1) {
		out[m[1]] = true
	}
	return out
}

// CheckError checks a failed run under --json: stdout empty, and stderr's
// last line {"error": {code, message, hint, doc_url}} with a documented
// code (earlier stderr lines are NDJSON events). It returns the error.
func CheckError(r Run) (map[string]any, error) {
	if r.OK() {
		return nil, fmt.Errorf("tw %s succeeded; want an error", strings.Join(r.Args, " "))
	}
	if strings.TrimSpace(r.Stdout) != "" {
		return nil, fmt.Errorf("tw %s failed but wrote to stdout:\n%s", strings.Join(r.Args, " "), r.Stdout)
	}
	ls, err := Lines(r.Stderr)
	if err != nil {
		return nil, fmt.Errorf("tw %s: stderr is not NDJSON: %v", strings.Join(r.Args, " "), err)
	}
	if len(ls) == 0 {
		return nil, fmt.Errorf("tw %s: no error on stderr", strings.Join(r.Args, " "))
	}
	last := ls[len(ls)-1]
	if err := CheckDoc("error", last); err != nil {
		return nil, err
	}
	e := last["error"].(map[string]any)
	if code, _ := e["code"].(string); !KnownCodes()[code] {
		return nil, fmt.Errorf("tw %s: error code %q is not in docs/errors.md", strings.Join(r.Args, " "), code)
	}
	return e, nil
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
