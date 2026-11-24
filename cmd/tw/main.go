// tailworks is the tailworks CLI (M4): an all-in-one homelab control plane on this
// machine (`tw up` runs tw-mgmt + tw-hostd under a supervisor in
// ~/.tailworks), app scaffolding and deploys, and the day-2 commands (kill,
// audit, ls, logs, status).
//
//	tw up --oauth-client-file <file> [--service] --yes      # an OAuth client of your tailnet mints every node key
//	tw link tw-mgmt.<tailnet>.ts.net                       # a developer machine: every command below goes to mgmt over the tailnet (WhoIs)
//	tw init hello --template agent && cd hello && tw deploy
//	tw ls · tw kill <object> --cascade · tw audit <object> · tw logs <workload>
//	tw down
//
// tailworks never uses this machine's tailscaled or its identity: every node is
// a tsnet node joined with a key minted for the tailnet you name.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/termsafe"
	"github.com/rosskukulinski/tailworks/netd"
)

type command struct {
	name, usage, help string
	run               func(args []string) error
	hidden            bool

	// `tw help` (helpgroups.go): the command's group and its one line
	// there (≤ 60 characters). Every visible command sets both, on its own
	// line above, so a new command never edits a shared list.
	group helpGroup
	short string
}

var commands []command

func init() {
	commands = []command{
		{name: "up", group: groupSetUp, short: "start tailworks on this machine", usage: "up --oauth-client-file <file> [--service] [--yes]  |  up --control=headscale --control-url <url> --tailnet <base_domain> --api-key-file <file> [--policy-apply]", help: "start tailworks on this machine (one process: mgmt + embedded host; --separate-host for the two-node layout); an OAuth client of your tailnet mints every node key (Headscale: its API key)", run: cmdUp},
		{name: "down", group: groupSetUp, short: "stop everything tw up started", usage: "down [--purge] [--service]", help: "stop everything tw up started (--purge: kill every object and delete its device first)", run: cmdDown},
		{name: "link", group: groupSetUp, short: "use a remote tw-mgmt from this machine", usage: "link [tw-mgmt.<tailnet>.ts.net] [--unlink]", help: "developer machine: use a remote tw-mgmt over your own Tailscale connection (WhoIs identity; no key or token)", run: cmdLink},
		{name: "whoami", group: groupObserve, short: "who the CLI acts as, and what it may do", usage: "whoami", help: "who this CLI acts as (WhoIs through tw link, or the local operator), its tenant, the mgmt it talks to, and its github.com/rosskukulinski/tailworks/cap/* capabilities: admin, approve, MCP tools", run: cmdWhoami},
		{name: "status", group: groupObserve, short: "health of mgmt, hosts, tenants, deployments", usage: "status", help: "health of mgmt, the host, the tenant and deployments", run: cmdStatus},
		{name: "tenant", group: groupSetUp, short: "serve, list, update or remove tenants", usage: "tenant add <tailnet> --oauth-client-file <file> [--policy-apply] [--owner-tag <tag> --name-prefix <p>] | tenant ls | tenant set <tenant> [--owner-tag <tag>] [--name-prefix <p>] [--max-objects N] [--registrations-per-minute N] [--pool P] [--tier T] [--oauth-client-file FILE] [--max-containers N] [--max-container-mem-mib N] [--max-container-vcpu F] [--max-store-bytes SIZE] [--max-queue-bytes SIZE] [--max-stores N] [--public-ingress allow|deny] | tenant rm <tenant> [--force]", help: "serve another tenant (its own tailnet) from this host; list, update (PATCH) or remove tenants", run: cmdTenant},
		{name: "init", group: groupDevelop, short: "create an app from a template", usage: "init <dir> [--template hello|agent|mcp|ai-agent|chat-agent|pi-agent|lock|refund-approval|node-workflow] [--name <workload>]", help: "create an app from a template (node-workflow needs Node.js on the deploying machine)", run: cmdInit},
		{name: "dev", group: groupDevelop, short: "run an app locally, no tailnet", usage: "dev [dir] [--port 8787] [--caller <name>[=<tag>,…]]… [--env <name>] [--wrangler | --from-wrangler-out <dir>]", help: "run an app locally: process tier + embedded control plane (no tailnet)", run: cmdDev},
		{name: "types", group: groupDevelop, short: "generate Env types from tailworks.jsonc and wrangler config", usage: "types [dir] [--env <name>] [--out worker-configuration.d.ts] [--check] [--remote]", help: "write the app's TypeScript Env (vars, Durable Object, KV, R2, D1, Queue and Workflow bindings, secret names) from its wrangler config and tailworks.jsonc, like wrangler types; --check fails when the file is stale (CI)", run: cmdTypes},
		{name: "deploy", group: groupDeploy, short: "bundle, place and deploy an app; wait until ready", usage: "deploy [dir] [--tenant <tailnet>] [--name <workload>] [--env <name>] [--wrangler | --from-wrangler-out <dir>] [--host <id>] [--replace] [--force-takeover] [--allow-missing-grants] [--apply-policy [--yes]] [--no-wait] [--platform linux/amd64,linux/arm64] [--wait-public 5m] [--preview] [--preview-alias <alias>]", help: "bundle, preflight grants, build + push Dockerfile container images (docker buildx), place (prints why), deploy, wait until ready; --replace re-places a running workload; --preview runs it next to the workload instead (tw promote)", run: cmdDeploy},
		{name: "policy", group: groupDeploy, short: "the tailnet policy an app needs, before deploying", usage: "policy [dir] [--tenant <tailnet>] [--name <workload>] [--env <name>] [--wrangler | --from-wrangler-out <dir>] [--apply [--yes]]", help: "print everything an app needs in the tailnet policy as one snippet: missing tagOwners, tailnet.connects grants, service auto-approval and public nodeAttrs (checked against the live policy, like tw deploy's preflight; nothing is deployed), plus the grant that lets people call it; exits acl_denied when a checked part is missing; --apply writes the missing tag:tw* lines itself (additive, If-Match)", run: cmdPolicy},
		{name: "deployments", group: groupDeploy, short: "a workload's deployment history", usage: "deployments <workload> [--tenant <tailnet>] [--limit N]", help: "a workload's deployment history, newest first: id, created, bundle sha256, status, host, who deployed (mgmt keeps the newest bundles for rollback)", run: cmdDeployments},
		{name: "previews", group: groupDeploy, short: "a workload's running preview deploys", usage: "previews <workload> [--tenant <tailnet>]", help: "a workload's running previews (tw deploy --preview): alias, created, bundle sha256, status, host, URL", run: cmdPreviews},
		{name: "promote", group: groupDeploy, short: "make a preview the workload's deployment", usage: "promote <workload> <preview> [--tenant <tailnet>] [--host <id>] [--replace] [--force-takeover] [--keep-preview] [--allow-missing-grants] [--no-wait]", help: "deploy a preview's bundle to the workload as a new deployment, then undeploy the preview; code and config only, the workload keeps its data and secrets", run: cmdPromote},
		{name: "rollback", group: groupDeploy, short: "redeploy an earlier deployment's bundle", usage: "rollback <workload> [<deployment>] [--tenant <tailnet>] [--host <id>] [--replace] [--force-takeover] [--allow-missing-grants] [--no-wait]", help: "redeploy an earlier deployment's stored bundle as a new deployment (default: the previous one that ran); code and config only, data and secrets stay as they are now", run: cmdRollback},
		{name: "ls", group: groupObserve, short: "list deployments and objects", usage: "ls [--tenant <tailnet>] [--all] [--limit N]", help: "list deployments and objects (paged; --all follows every page and shows killed objects)", run: cmdLs},
		{name: "undeploy", group: groupDeploy, short: "stop a workload and release its lease", usage: "undeploy <workload> [--tenant <tailnet>] [--reason <text>]", help: "stop a workload: kill its objects (cascade), release its lease", run: cmdUndeploy},
		{name: "expose", group: groupDeploy, short: "open or close a workload's public routes", usage: "expose on|off <workload> [--tenant <tailnet>]", help: "open or close a workload's public (Funnel) routes; survives redeploys (tailworks.jsonc \"public\", ADR-029)", run: cmdExpose},
		{name: "secret", group: groupDeploy, short: "a workload's secrets (env.NAME)", usage: "secret put <NAME> | secret ls | secret rm <NAME>   [--workload <workload>] [--tenant <tailnet>]", help: "a workload's secrets (env.NAME, like wrangler secret): put reads the value from stdin (hidden prompt on a terminal), ls shows names only; changes apply on the next deploy", run: cmdSecret},
		{name: "d1", group: groupData, short: "a workload's D1 databases and migrations", usage: d1Usage, help: "a workload's D1 databases (like wrangler d1): create, list, delete, run SQL, export, and wrangler-layout migrations; execute/export/migrations reach the running workload", run: cmdD1},
		{name: "kv", group: groupData, short: "a workload's Workers KV namespaces and keys", usage: "kv namespace create <NAME> | kv namespace list | kv namespace delete <NAME> | kv key put <KEY> [VALUE] [--path FILE] [--ttl S] [--expiration T] [--metadata JSON] | kv key get <KEY> | kv key delete <KEY> | kv key list [--prefix P]   [--binding <BINDING> or --namespace <NAME>] [--workload <workload>] [--tenant <tailnet>]", help: "a workload's Workers KV namespaces (kv_namespaces, like wrangler kv): key commands name the namespace by --binding (from the wrangler config) or --namespace; writes reach running workloads within 60 s", run: cmdKV},
		{name: "queues", group: groupData, short: "the tenant's queues: create, peek, send", usage: "queues create <queue> | queues ls | queues info <queue> | queues rm <queue> | queues purge <queue> | queues messages <queue> [--limit N] | queues send <queue> [<json>|--text <text>] [--delay S]   [--tenant <tailnet>]", help: "the tenant's queues (like wrangler queues): producers' first send creates a queue; messages peeks without claiming (read a dead-letter queue); send takes JSON (or --text) from the argument or stdin", run: cmdQueues},
		{name: "r2", group: groupData, short: "a workload's R2 buckets and objects", usage: r2Usage, help: "a workload's R2 buckets (r2_buckets bindings): list, create or delete buckets, list a bucket's objects", run: cmdR2},
		{name: "telemetry", group: groupObserve, short: "the tenant's OTLP export", usage: "telemetry [show] | telemetry set --otlp <url> [--header NAME[=VALUE]]... [--signals logs,metrics,traces] | telemetry rm   [--tenant <tailnet>]", help: "the tenant's OTLP export of request records, metrics and spans (each workload exports from its own node); --header NAME reads a credential from stdin, never shown again", run: cmdTelemetry},
		{name: "kill", group: groupOperate, short: "kill an object (--cascade: its sub-agents)", usage: "kill <object> [--tenant <tailnet>] [--cascade] [--reason <text>]", help: "kill an object (and with --cascade its sub-agents)", run: cmdKill},
		{name: "approvals", group: groupOperate, short: "Workflow approvals waiting for a decision", usage: "approvals [workload] [--tenant <tailnet>] [--all]", help: "Workflow approvals waiting for a decision (with age and the instance's params)", run: cmdApprovals},
		{name: "approve", group: groupOperate, short: "approve or reject a waiting Workflow", usage: "approve <workload> <instance> [--reject] [--comment <text>] [--workflow <name>] [--step <name>] [--tenant <tailnet>]", help: "approve (or --reject) a waiting Workflow as your own tailnet identity (needs a github.com/rosskukulinski/tailworks/cap/approve grant; from a `tw link`ed machine)", run: cmdApprove},
		{name: "notify", group: groupOperate, short: "a webhook for approvals waiting for a decision", usage: "notify [show] | notify set --webhook <url|-> [--header NAME[=VALUE]]... [--events approval_requested] | notify test | notify rm   [--tenant <tailnet>]", help: "tell people when a Workflow waits for an approval: mgmt POSTs JSON with a link to the approvals page (Slack and chat incoming webhooks show its text); the URL is a secret, never shown again", run: cmdNotify},
		{name: "audit", group: groupObserve, short: "the hash-chained audit trail", usage: "audit [--tenant <tailnet>] <object> | --human <login> | --trace <id> | --all", help: "show the hash-chained audit trail", run: cmdAudit},
		{name: "logs", group: groupObserve, short: "request records and console lines; daemon logs", usage: "logs [workload] [--tenant <tailnet>] [--component mgmt|hostd|supervisor] [--host <id>] [-n 50] [-f] [--json] [--console] [--object <id|Class/name>] [--status 5xx] [--outcome exception] [--errors] [--caller <login|node>] [--trace <id>]", help: "show a workload's request records (one line per request with its console.* lines; --json, filters; --console for the raw app output), else the daemons' logs", run: cmdLogs},
		{name: "doctor", group: groupSetUp, short: "check this machine can run tw", usage: "doctor", help: "check this machine: workerd runs, bundler offline, ~/.tailworks writable, the mint credential works, process sandbox active, system tailscaled not used; on Linux also glibc, kernel/Landlock ABI, KVM, firecracker, cgroups v2, systemd and the microVM tier", run: cmdDoctor},
		{name: "docs", group: groupDevelop, short: "search and read tailworks' docs offline", usage: "docs search <words…> [--limit 10] | docs show <doc>[#<anchor>] | docs ls", help: "tailworks' docs, offline: search them, print one doc or section (e.g. errors.md#acl_denied)", run: cmdDocs},
		{name: "completion", group: groupOperate, short: "shell completion script (bash, zsh, fish)", usage: "completion bash|zsh|fish", help: "print a shell completion script for every command, subcommand and flag (bash: eval \"$(tw completion bash)\"; zsh: tw completion zsh > \"${fpath[1]}/_tw\"; fish: tw completion fish > ~/.config/fish/completions/tw.fish)", run: cmdCompletion},
		{name: "mcp", group: groupDevelop, short: "tailworks' MCP server for coding agents (stdio)", usage: "mcp [--allow deploy,kill,undeploy] | mcp --config", help: "tailworks' MCP server for coding agents, over stdio (a bridge to mgmt's /mcp; tools: whoami status ls logs audit, and deploy kill undeploy when allowed); --config prints how to register it", run: cmdMCP},
		{name: "agent", group: groupDevelop, short: "teach coding agents to drive tw (skills)", usage: "agent setup [--scope user|project] [--dir <dir>] [--agent claude,codex,cursor,gemini,opencode] [--dry-run] [--yes] | agent remove [same flags] | agent ls | agent show <skill>", help: "teach coding agents (Claude Code, Codex, Cursor, Gemini CLI, OpenCode) to drive tw: install or remove tailworks' skills, list or print them", run: cmdAgent},
		{name: "host", group: groupSetUp, short: "add, list, remove or set up hosts", usage: "host add <host-id> [--tenant <tailnet>] [--out <file>] | host ls | host rm <host-id> [--drain] | host cordon <host-id> | host uncordon <host-id> | host drain <host-id> [--allow-local-state-loss] [--dry-run] | host setup --microvm [--user <name>] [--hugepages N] | host setup --undo   (Linux, with sudo, once) | host vmcheck <workload> [--keyscan] [--container <Class>/<name>]", help: "add a remote host (mgmt mints its single-use tag:tw-host join key), list or remove hosts; cordon (no new placements), uncordon, or drain one (move its workloads elsewhere); install the microVM tier's helper; check a microVM", run: cmdHost},
		{name: "image", group: groupDeploy, short: "public base images and operator images", usage: "image public add <ref> [--arch amd64,arm64] | image public ls | image public rm <id> | image push <oci-layout|docker-save.tar> tw.local/<name>:<tag>", help: "container images (ADR-032): mark public base images shared across tenants (mirrored now), push operator images (the arm64 alias targets) to tailworks' registry", run: cmdImage},
		{name: "backup", group: groupOperate, short: "back up mgmt's store, custody and tw.json, online", usage: "backup [--out <dir>]", help: "back up this install's control plane while it runs: mgmt's store (a consistent SQLite copy), custody, the ID secret, mgmt's node state and tw.json, into a new directory (tw restore puts it back)", run: cmdBackup},
		{name: "restore", group: groupOperate, short: "put a tw backup back (tailworks stopped)", usage: "restore <backup-dir> [--yes]", help: "restore a tw backup (or a tw upgrade backup) with tailworks stopped: the current state is kept in <state>/backups first, then the restore fence moves every lease epoch past any handed out since the backup", run: cmdRestore},
		{name: "upgrade", group: groupOperate, short: "upgrade tailworks on this machine or the fleet", usage: upgradeUsage, help: "upgrade tailworks: on a tw up machine, back up, install a newer release and roll back if it isn't healthy; against a linked tw-mgmt (or --fleet), plan and run a fleet upgrade (status, pause, resume, abort)", run: cmdUpgrade},
		{name: "release", group: groupOperate, short: "tw-mgmt's release catalog", usage: releaseUsage, help: "tw-mgmt's release catalog: list known releases, import a release (its notes and tarball, for hosts that upgrade themselves)", run: cmdRelease},
		{name: "version", group: groupOperate, short: "print the version; --check for a newer release", usage: "version", help: "print the tw version (and the pinned workerd / built-in esbuild)", run: cmdVersion},
		{name: "__supervise", hidden: true, run: cmdSupervise},
	}
}

// usageAll is `tw help --all`: every command with its full usage line
// (`tw help` is usageGrouped, helpgroups.go).
func usageAll(w io.Writer) {
	fmt.Fprintln(w, "tw: Workers and Durable Objects with their own tailnet identities")
	fmt.Fprintln(w, "\nusage: tw <command> [flags]   (every command takes --state-dir, default ~/.tailworks, and --json)")
	fmt.Fprintln(w, "       tw <command> --help     usage of one command")
	fmt.Fprintln(w)
	for _, c := range commands {
		if !c.hidden {
			fmt.Fprintf(w, "  %-8s %s\n           tw %s\n", c.name, c.help, c.usage)
		}
	}
	fmt.Fprintln(w, "\ntemplates (tw init --template): hello | agent | mcp | ai-agent | chat-agent | pi-agent | lock | refund-approval | node-workflow (node-workflow needs Node.js on the deploying machine)")
}

// commandUsage prints one command's help (`tw <cmd> --help`): the usage
// line and, when the command's flag set is known (f != nil), every flag
// with its default.
func commandUsage(w io.Writer, c command, f *flags) {
	fmt.Fprintf(w, "tw %s: %s\n\nusage: tw %s\n", c.name, c.help, c.usage)
	if f == nil {
		fmt.Fprintln(w, "  (every command takes --state-dir, default ~/.tailworks, and --json)")
		return
	}
	fmt.Fprintln(w, "\nflags:")
	plainUsages(f.FlagSet) // didyoumean.go
	f.SetOutput(w)
	f.PrintDefaults()
	f.SetOutput(io.Discard)
}

// commandHelp runs c with args that ask for help: every command builds its
// flag set and parses before doing anything else, so it stops at -h/--help
// and hands the flag set back (helpRequest), which commandUsage lists.
// Commands that dispatch on a subcommand first (host, tenant) return a
// usage error for a bare --help; then only the usage line is printed.
func commandHelp(w io.Writer, c command, args []string) {
	var hr *helpRequest
	if err := c.run(args); errors.As(err, &hr) {
		commandUsage(w, c, hr.f)
		return
	}
	commandUsage(w, c, nil)
}

// errHelp is returned (wrapped in a helpRequest) by flags.parse when
// -h/--help was given; main prints the command's usage to stdout and
// exits 0.
var errHelp = errors.New("help requested")

// helpRequest carries the flag set that saw -h/--help, so the usage can
// list the command's flags. errors.Is(err, errHelp) holds for it.
type helpRequest struct{ f *flags }

func (h *helpRequest) Error() string        { return errHelp.Error() }
func (h *helpRequest) Is(target error) bool { return target == errHelp }

// isHelpFlag reports whether a is -h, -help or --help.
func isHelpFlag(a string) bool { return a == "-h" || a == "-help" || a == "--help" }

// wantsHelp reports whether args ask for help before a `--`.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if isHelpFlag(a) {
			return true
		}
	}
	return false
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process exit (tests call it): the exit code is
// 0 on success and for any help request, 1 for a failed command, 2 for a
// usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		args = []string{"version"}
	}
	if len(args) == 0 || isHelpFlag(args[0]) || args[0] == "help" {
		// `tw help <cmd>` is `tw <cmd> --help`.
		if len(args) == 2 && args[0] == "help" {
			for _, c := range commands {
				if c.name == args[1] && !c.hidden {
					commandHelp(stdout, c, []string{"--help"})
					return 0
				}
			}
			if args[1] == "--all" {
				usageAll(stdout)
				return 0
			}
		}
		usage(stdout)
		return 0
	}
	// Allow global flags before the command: tw --state-dir X <cmd> …
	var pre []string
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		pre = append(pre, args[0])
		if !strings.Contains(args[0], "=") && len(args) > 1 && (args[0] == "--state-dir" || args[0] == "-state-dir") {
			pre = append(pre, args[1])
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 {
		if wantsHelp(pre) {
			usage(stdout)
			return 0
		}
		usage(stderr)
		return 2
	}
	name, rest := args[0], append(pre, args[1:]...)
	jsonOut = wantsJSON(rest) // agentjson.go: usage errors are JSON too
	for _, c := range commands {
		if c.name == name {
			if wantsHelp(rest) && !c.hidden {
				commandHelp(stdout, c, rest)
				return 0
			}
			if err := c.run(rest); err != nil {
				var hr *helpRequest
				if errors.As(err, &hr) {
					commandUsage(stdout, c, hr.f)
					return 0
				}
				printErr(err)
				return 1
			}
			updateNotice(name, stderr) // updatecheck.go
			return 0
		}
	}
	printErr(unknownCommand(name)) // didyoumean.go
	return 2
}

// ---- errors (the typed error catalog, docs/errors.md) ----

// cliErr is a typed error printed in the catalog shape (docs/errors.md):
//
//	error [code]: message
//	  detail…
//	  fix: …
//	  https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#<code>
type cliErr struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Lines   []string `json:"detail,omitempty"`
	Fix     []string `json:"fix,omitempty"`
	TraceID string   `json:"trace_id,omitempty"`

	// Hint and DocURL are filled in by printErr for --json (agentjson.go):
	// every error object has code, message and hint.
	Hint   string `json:"hint"`
	DocURL string `json:"doc_url"`
}

func (e *cliErr) Error() string { return e.Code + ": " + e.Message }

func fail(code, msg string, lines ...string) *cliErr {
	return &cliErr{Code: code, Message: msg, Lines: lines}
}

func (e *cliErr) withFix(fix ...string) *cliErr { e.Fix = append(e.Fix, fix...); return e }

// jsonOut is set by --json: errors are printed as JSON too.
var jsonOut bool

func asCLIErr(err error) *cliErr {
	var ce *cliErr
	if errors.As(err, &ce) {
		return ce
	}
	if me, ok := mgmtapi.AsError(err); ok {
		e := fail(me.Code, me.Message)
		if me.Snippet != "" {
			e.Fix = append(e.Fix, "add to your tailnet policy:", "  "+me.Snippet)
		}
		if len(me.Missing) > 0 {
			b, _ := json.Marshal(me.Missing)
			e.Lines = append(e.Lines, "missing: "+string(b))
		}
		e.TraceID = me.TraceID
		return e
	}
	return fail("internal", netd.Redact(err.Error()))
}

func printErr(err error) {
	e := asCLIErr(err)
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"error": jsonError(e)})
		fmt.Fprintln(os.Stderr, netd.Redact(string(termsafe.JSON(b))))
		return
	}
	// The message and lines can quote mgmt's or a host's error text, and
	// through it a tenant's or an internet client's (a bundler error, a
	// deploy error): escape them, keeping their line breaks.
	fmt.Fprintf(os.Stderr, "error [%s]: %s\n", e.Code, netd.Redact(termsafe.Text(e.Message)))
	for _, l := range e.Lines {
		fmt.Fprintf(os.Stderr, "  %s\n", netd.Redact(termsafe.Text(l)))
	}
	for i, l := range e.Fix {
		if i == 0 {
			fmt.Fprintf(os.Stderr, "  fix: %s\n", l)
		} else {
			fmt.Fprintf(os.Stderr, "    %s\n", l)
		}
	}
	tr := ""
	if e.TraceID != "" {
		tr = "trace " + e.TraceID + " · "
	}
	fmt.Fprintf(os.Stderr, "  %s%s%s\n", tr, netd.DocBase, e.Code)
}

// ---- flags ----

type flags struct {
	*flag.FlagSet
	stateDir *string
	json     *bool
}

func newFlags(name string) *flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := &flags{FlagSet: fs}
	f.stateDir = fs.String("state-dir", "", "state directory (default $TW_STATE_DIR or ~/.tailworks)")
	f.json = fs.Bool("json", false, "machine-readable output")
	return f
}

// parse parses flags interleaved with positional args (tw kill X --cascade).
func (f *flags) parse(args []string) ([]string, error) {
	var pos []string
	for {
		if err := f.FlagSet.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, &helpRequest{f: f}
			}
			return nil, fail("bad_request", err.Error(), "usage: tw "+usageOf(f.Name()))
		}
		args = f.FlagSet.Args()
		if len(args) == 0 {
			break
		}
		if args[0] == "--" {
			pos = append(pos, args[1:]...)
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	jsonOut = *f.json
	return pos, nil
}

func usageOf(name string) string {
	for _, c := range commands {
		if c.name == name {
			return c.usage
		}
	}
	return name
}

// emit prints v as JSON (with --json); otherwise human() runs.
func emit(f *flags, v any, human func()) {
	if *f.json {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(netd.Redact(string(termsafe.JSON(b))))
		return
	}
	human()
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
