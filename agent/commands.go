package agent

import (
	"fmt"
	"strings"
)

// The commands a skill tells an agent to run, as tests read them:
//
//   - every `tw …` invocation in a sh/bash/shell code block, and inline
//     code spans starting with "tw ", must name a real command and real
//     flags (cmd/tw TestSkillCommandsExist);
//   - a code block right after an "<!-- tw-eval: <step> -->" comment is
//     replayed verbatim, in step order, against a running tailworks by the eval
//     test (cmd/tw TestAgentEval, e2e/agenteval).

// Command is one shell command from a skill.
type Command struct {
	Skill string
	Line  int    // 1-based line in SKILL.md
	Text  string // the command as written
	// Args are the words after "tw" (quotes removed); nil for `cd`.
	Args []string
	// Cd is the directory of a `cd <dir>` step.
	Cd string
	// Inline: from a `code span` in prose, not a code block.
	Inline bool
}

// EvalStep is a replayable block: <!-- tw-eval: <Name> -->.
type EvalStep struct {
	Name     string
	Skill    string
	Commands []Command
}

const evalPrefix = "<!-- tw-eval: "

// Commands returns every tailworks command in a skill (blocks and inline code),
// and its eval steps.
func Commands(s Skill) ([]Command, []EvalStep, error) {
	var cmds []Command
	var steps []EvalStep
	lines := strings.Split(s.Body, "\n")
	inBlock, shell := false, false
	var step *EvalStep
	pendingEval := ""
	for i, l := range lines {
		ln := i + 1
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			if inBlock {
				inBlock = false
				if step != nil {
					steps = append(steps, *step)
					step = nil
				}
				continue
			}
			inBlock = true
			lang := strings.TrimPrefix(t, "```")
			shell = lang == "sh" || lang == "bash" || lang == "shell"
			if pendingEval != "" {
				if !shell {
					return nil, nil, fmt.Errorf("%s:%d: tw-eval block must be sh", s.Name, ln)
				}
				step = &EvalStep{Name: pendingEval, Skill: s.Name}
				pendingEval = ""
			}
			continue
		}
		if inBlock {
			if !shell {
				continue
			}
			for _, c := range splitShell(t) {
				c.Skill, c.Line = s.Name, ln
				if c.Args == nil && c.Cd == "" {
					continue
				}
				cmds = append(cmds, c)
				if step != nil {
					step.Commands = append(step.Commands, c)
				}
			}
			continue
		}
		if name, ok := strings.CutPrefix(t, evalPrefix); ok {
			pendingEval = strings.TrimSpace(strings.TrimSuffix(name, "-->"))
			continue
		}
		if pendingEval != "" && t != "" {
			return nil, nil, fmt.Errorf("%s:%d: tw-eval comment must be followed by a code block", s.Name, ln)
		}
		for _, span := range codeSpans(l) {
			if !strings.HasPrefix(span, "tw ") {
				continue
			}
			args, err := words(strings.TrimPrefix(span, "tw "))
			if err != nil {
				return nil, nil, fmt.Errorf("%s:%d: %w", s.Name, ln, err)
			}
			cmds = append(cmds, Command{Skill: s.Name, Line: ln, Text: span, Args: args, Inline: true})
		}
	}
	if inBlock {
		return nil, nil, fmt.Errorf("%s: unterminated code block", s.Name)
	}
	return cmds, steps, nil
}

// splitShell splits one shell line at |, && and ;, drops a trailing
// comment, and keeps the `tw …` and `cd …` parts.
func splitShell(line string) []Command {
	if i := commentStart(line); i >= 0 {
		line = line[:i]
	}
	var out []Command
	for _, part := range splitOps(line) {
		part = strings.TrimSpace(part)
		w, err := words(part)
		if err != nil || len(w) == 0 {
			continue
		}
		switch w[0] {
		case "tw":
			out = append(out, Command{Text: part, Args: append([]string{}, w[1:]...)})
		case "cd":
			if len(w) == 2 {
				out = append(out, Command{Text: part, Cd: w[1]})
			}
		}
	}
	return out
}

// commentStart is the index of a " #" comment outside quotes, or -1.
func commentStart(s string) int {
	q := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return i
		}
	}
	return -1
}

// splitOps splits at |, ||, && and ; outside quotes.
func splitOps(s string) []string {
	var out []string
	q := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '|' || c == ';' || (c == '&' && i+1 < len(s) && s[i+1] == '&'):
			out = append(out, s[start:i])
			if i+1 < len(s) && (s[i+1] == '|' || s[i+1] == '&') {
				i++
			}
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// words splits on spaces, honouring '…' and "…" quoting.
func words(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, q := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			q, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if q != 0 {
		return nil, fmt.Errorf("unbalanced quote in %q", s)
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// codeSpans returns the `inline code` spans of a Markdown line.
func codeSpans(l string) []string {
	var out []string
	for {
		i := strings.IndexByte(l, '`')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(l[i+1:], '`')
		if j < 0 {
			return out
		}
		out = append(out, l[i+1:i+1+j])
		l = l[i+1+j+1:]
	}
}
