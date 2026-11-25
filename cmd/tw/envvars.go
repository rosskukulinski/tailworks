package main

import (
	"fmt"
	"strings"
)

// The environment variables tw reads (issue #528), rendered as the
// `## Environment` section at the end of docs/cli.md (cliReference).
// TestCLIReferenceEnvVars fails when the Go sources of the tw binary
// read a TW_* variable that isn't listed here, or this lists one nothing
// reads, so the section can't drift.

// envVar is one row of the Environment table.
type envVar struct {
	Name string
	What string
	// Flag is the flag the variable is the environment form of ("" if none).
	Flag string
	// Scripts: get.sh and install.sh read it too (with the same meaning).
	Scripts bool
	// ScriptsOnly: only get.sh / install.sh read it, not the tw binary.
	ScriptsOnly bool
}

// envVars are the user-facing variables, in the order the table shows them.
var envVars = []envVar{
	{
		Name: "TW_STATE_DIR",
		What: "the state dir (default `~/.tailworks`). A `tw up --service` unit passes `--state-dir` itself.",
		Flag: "`--state-dir`",
	},
	{
		Name:        "TW_HOME",
		What:        "where `get.sh` and `install.sh` install the release (the prefix; default `~/.tailworks`). It shares the state dir's default but is a different thing: [What is in ~/.tailworks](INSTALL.md#what-is-in-tailworks).",
		Flag:        "`install.sh --prefix`",
		ScriptsOnly: true,
	},
	{
		Name:        "TW_INSTALL_FORCE",
		What:        "`1`: `get.sh` replaces another version while tailworks runs from the prefix, with no backup or health check. Use `tw upgrade` instead.",
		Flag:        "`install.sh --force`",
		ScriptsOnly: true,
	},
	{
		Name: "TW_NO_UPDATE_CHECK",
		What: "set (to `1`): no once-a-day \"newer release\" notice after a command. `CI` does the same.",
	},
	{
		Name: "CI",
		What: "set (as CI systems do): no update notice, as with `TW_NO_UPDATE_CHECK`.",
	},
	{
		Name: "TW_WORKERD",
		What: "the workerd binary, when it isn't next to `tw` or in `~/.tailworks/bin`.",
		Flag: "`--workerd` (`tw up`, `tw dev`)",
	},
	{
		Name: "TW_NODE",
		What: "the Node.js binary that runs `\"runtime\": \"node\"` workloads (default: `node` on `PATH`).",
	},
	{
		Name: "TW_CREATE_TAILWORKS",
		What: "the create-tailworks package directory (bundler and templates), when it isn't in the install or a checkout.",
	},
	{
		Name: "TW_WRANGLER",
		What: "the wrangler CLI that builds a wrangler project (default: the project's `node_modules/.bin/wrangler`, then `PATH`).",
	},
	{
		Name:    "TW_REPO",
		What:    "the GitHub repository releases come from (default `rosskukulinski/tailworks`), for `tw upgrade` and `tw version --check`.",
		Scripts: true,
	},
	{
		Name:    "TW_RELEASE_BASE",
		What:    "a mirror to download release tarballs and `SHA256SUMS` from instead of GitHub (the per-tag directory). The signature check still applies.",
		Scripts: true,
	},
	{
		Name:    "TW_RELEASE_API",
		What:    "a mirror of the GitHub API that answers \"latest release\" (default `https://api.github.com`), for `tw upgrade`, `tw version --check` and `get.sh`. For tests and private mirrors.",
		Scripts: true,
	},
	{
		Name:    "TW_RELEASE_SIGNERS",
		What:    "an `allowed_signers` file that replaces the pinned release keys. For tests and private builds; a host's self-upgrade (`tw-hostd`) ignores it.",
		Scripts: true,
	},
	{
		Name:    "TW_ALLOW_UNSIGNED",
		What:    "`1`: the per-invocation form of `--allow-unsigned`, for a build you made yourself. Don't export it in a shell profile: every later `get.sh`, `install.sh` and `tw upgrade` would skip the release signature check ([security-model.md](security-model.md)). A host's self-upgrade ignores it.",
		Flag:    "`--allow-unsigned`",
		Scripts: true,
	},
}

// envDebugVars are debugging and test-harness knobs: the footer names them,
// they aren't rows. What continues a sentence that starts with the name.
var envDebugVars = []envVar{
	{
		Name: "TW_KEEP_INSTANCES",
		What: ", when set, keeps a workload instance's directory after it stops; its VM disks and tenant secrets stay on disk, so never set it on a production host.",
	},
	{
		Name: "TW_TEST_LINK_NAME",
		What: " is the name `tw link` discovers instead of `tw-mgmt`, for hermetic tests.",
	},
}

// envReference is docs/cli.md's `## Environment` section.
func envReference() string {
	var b strings.Builder
	b.WriteString("\n## Environment\n\n")
	b.WriteString("The variables `tw` reads, and the ones `get.sh` and `install.sh` read\n")
	b.WriteString("(\"scripts\"). A flag, where there is one, wins over the variable. The\n")
	b.WriteString("`tw up --service` unit sets only `PATH` and passes `--state-dir`, so\n")
	b.WriteString("none of these reach a tailworks that runs as a service unless the unit\n")
	b.WriteString("sets them. `TW_*` and `__TW*` names are reserved: a Worker never sees\n")
	b.WriteString("them, and secrets and `node.env` can't use them.\n\n")
	b.WriteString("| Variable | What it does | Flag | Read by |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range envVars {
		by := "tw"
		switch {
		case v.ScriptsOnly:
			by = "scripts"
		case v.Scripts:
			by = "tw, scripts"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", v.Name, v.What, v.Flag, by)
	}
	b.WriteString("\nFor debugging and the test harness only:")
	for _, v := range envDebugVars {
		fmt.Fprintf(&b, " `%s`%s", v.Name, v.What) // What continues the sentence
	}
	b.WriteString("\n")
	return b.String()
}
