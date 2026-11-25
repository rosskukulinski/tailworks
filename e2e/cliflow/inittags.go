package cliflow

// AgentObjectTags are tw init flags that keep the agent template's Durable
// Object class tags, which Policy owns, instead of the per-app tags tw init
// names after the workload (tag:tw-<workload>-<class>). tw init names the
// workload tag after the app either way (tag:tw-wl-agent for an "agent"
// directory).
var AgentObjectTags = []string{
	"--object-tags", "SupportAgent=tag:tw-agent-support",
	"--object-tags", "LookupAgent=tag:tw-agent-support-sub",
}

// InitArgs is `tw init <dir> --template <tmpl>`, with AgentObjectTags for the
// agent template.
func InitArgs(dir, tmpl string) []string {
	args := []string{"init", dir, "--template", tmpl}
	if tmpl == "agent" {
		args = append(args, AgentObjectTags...)
	}
	return args
}
