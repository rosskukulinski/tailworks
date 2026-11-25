package bundle

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Workflow is one wrangler.jsonc "workflows" entry: a WorkflowEntrypoint
// class run by workerd's built-in Workflows engine (/workerd/workflows.js),
// reached as env.<Binding> (ADR-040).
type Workflow struct {
	Name    string // the Workflow's name (instances are keyed by it)
	Binding string // env binding name
	Class   string // class_name: the exported WorkflowEntrypoint class
	// Steps is limits.steps (0: the engine's default).
	Steps int
	// Retention is default_retention, as the engine takes it:
	// {"success": …, "error": …} (nil: the engine's default).
	Retention json.RawMessage
}

type wranglerWorkflows struct {
	Workflows []struct {
		Name       string `json:"name"`
		Binding    string `json:"binding"`
		ClassName  string `json:"class_name"`
		ScriptName string `json:"script_name"`
		Limits     *struct {
			Steps int `json:"steps"`
		} `json:"limits"`
		Schedules        json.RawMessage `json:"schedules"`
		DefaultRetention *struct {
			Success json.RawMessage `json:"success_retention"`
			Error   json.RawMessage `json:"error_retention"`
		} `json:"default_retention"`
	} `json:"workflows"`
}

// workflowNameRE is the engine's rule for Workflow names (workerd
// cli/wrangler.rs): 1-64 characters, a letter, digit or underscore first,
// then letters, digits, underscores or hyphens.
var workflowNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,63}$`)

// parseWorkflows reads wrangler.jsonc "workflows" into a.Workflows. It runs
// after the Durable Object bindings are known, so a name clash is caught.
func parseWorkflows(wb []byte, a *App) error {
	var w wranglerWorkflows
	if err := UnmarshalJSONC(wb, &w); err != nil {
		return fmt.Errorf("bundle: wrangler.jsonc: %w", err)
	}
	if len(w.Workflows) == 0 {
		return nil
	}
	if a.Runtime == RuntimeNode {
		return fmt.Errorf("bundle: workflows need the workerd runtime")
	}
	names, bindings := map[string]bool{}, map[string]bool{}
	for _, b := range a.Bindings {
		bindings[b.Name] = true
	}
	for _, b := range a.Stores {
		bindings[b.Binding] = true
	}
	for _, x := range w.Workflows {
		where := fmt.Sprintf("bundle: workflows %q", x.Name)
		if !workflowNameRE.MatchString(x.Name) {
			return fmt.Errorf("%s: Workflow names must be 1-64 characters long, start with a letter, number, or underscore, and may only contain letters, numbers, underscores, or hyphens", where)
		}
		if names[x.Name] {
			return fmt.Errorf("%s: declared twice", where)
		}
		names[x.Name] = true
		if !identRE.MatchString(x.Binding) || !identRE.MatchString(x.ClassName) {
			return fmt.Errorf("%s: bad binding %q or class_name %q", where, x.Binding, x.ClassName)
		}
		if strings.HasPrefix(x.Binding, "__TW") || strings.HasPrefix(x.ClassName, "Host_") {
			return fmt.Errorf("%s: names starting with Host_/__TW are reserved", where)
		}
		if bindings[x.Binding] {
			return fmt.Errorf("%s: binding %s is already a binding", where, x.Binding)
		}
		bindings[x.Binding] = true
		if a.Classes[x.ClassName] != nil {
			return fmt.Errorf("%s: class %s is a Durable Object class; a Workflow class extends WorkflowEntrypoint", where, x.ClassName)
		}
		if x.ScriptName != "" && x.ScriptName != a.Name {
			// The engine runs Workflows of this Worker only; reach another
			// workload's Workflow over the tailnet instead.
			return fmt.Errorf("%s: script_name (a Workflow of another Worker) is not supported", where)
		}
		if s := strings.TrimSpace(string(x.Schedules)); s != "" && s != "null" {
			return fmt.Errorf("%s: schedules is not supported (use triggers.crons and create the instance in scheduled())", where)
		}
		wf := Workflow{Name: x.Name, Binding: x.Binding, Class: x.ClassName}
		if x.Limits != nil {
			if x.Limits.Steps < 0 {
				return fmt.Errorf("%s: limits.steps must be positive", where)
			}
			wf.Steps = x.Limits.Steps
		}
		if r := x.DefaultRetention; r != nil {
			m := map[string]json.RawMessage{}
			if len(r.Success) > 0 {
				m["success"] = r.Success
			}
			if len(r.Error) > 0 {
				m["error"] = r.Error
			}
			if len(m) > 0 {
				wf.Retention, _ = json.Marshal(m)
			}
		}
		a.Workflows = append(a.Workflows, wf)
	}
	return nil
}

// WorkflowByName returns the app's Workflow called name, or nil.
func (a *App) WorkflowByName(name string) *Workflow {
	for i := range a.Workflows {
		if a.Workflows[i].Name == name {
			return &a.Workflows[i]
		}
	}
	return nil
}
