// twd is the twd CLI (M4): an all-in-one homelab control plane on this
// machine (`twd up` runs twd-mgmt + twd-hostd under a supervisor in
// ~/.twd), app scaffolding and deploys, and the day-2 commands (kill,
// audit, ls, logs, status).
//
//	twd up --oauth-client-file <file> [--service] --yes      # recommended (M12): OAuth client, no browser, no Funnel
//	twd up --wif                                             # keyless: tailnet login + WIF trust credential (ADR-026)
//	twd up --tailnet <tailnet> --api-key-file <file> --yes   # fallback: apikey mint mode (tests)
//	twd link twd-mgmt.<tailnet>.ts.net                       # a developer machine: every command below goes to mgmt over the tailnet (WhoIs)
//	twd init hello --template agent && cd hello && twd deploy
//	twd ls · twd kill <object> --cascade · twd audit <object> · twd logs <workload>
//	twd down
//
// twd never uses this machine's tailscaled or its identity: every node is
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

	"github.com/rosskukulinski/twd/hostd/mgmtapi"
	"github.com/rosskukulinski/twd/netd"
)

type command struct {
	name, usage, help string
	run               func(args []string) error
	hidden            bool
}

var commands []command

func init() {
	commands = []command{
		{name: "up", usage: "up --oauth-client-file <file> [--service] [--yes]  |  up --wif [--wif-client-id <id>]  |  up --tailnet <tailnet> --api-key-file <file> [--policy-apply]", help: "start twd on this machine (one process: mgmt + embedded host; --separate-host for the two-node layout); recommended: an OAuth client (no browser, no Funnel); --wif for the keyless browser flow, --api-key-file as the fallback", run: cmdUp},
		{name: "down", usage: "down [--purge] [--service]", help: "stop everything twd up started (--purge: kill every object and delete its device first)", run: cmdDown},
		{name: "link", usage: "link [twd-mgmt.<tailnet>.ts.net] [--unlink]", help: "developer machine: use a remote twd-mgmt over your own Tailscale connection (WhoIs identity; no key or token)", run: cmdLink},
		{name: "status", usage: "status", help: "health of mgmt, the host, the tenant and deployments", run: cmdStatus},
		{name: "tenant", usage: "tenant add <tailnet> --api-key-file <file> [--policy-apply] | tenant ls | tenant set <tenant> [--max-objects N] [--registrations-per-minute N] [--pool P] [--tier T] [--wif-client-id ID] [--max-containers N] [--max-container-mem-mib N] [--max-container-vcpu F] [--public-ingress allow|deny] | tenant rm <tenant> [--force]", help: "serve another tenant (its own tailnet) from this host; list, update (PATCH) or remove tenants", run: cmdTenant},
		{name: "init", usage: "init <dir> [--template hello|agent|mcp|ai-agent|lock|refund-approval|node-workflow] [--name <workload>]", help: "create an app from a template (node-workflow needs Node.js on the deploying machine)", run: cmdInit},
		{name: "dev", usage: "dev [dir] [--port 8787] [--env <name>] [--wrangler | --from-wrangler-out <dir>]", help: "run an app locally: process tier + embedded control plane (no tailnet)", run: cmdDev},
		{name: "deploy", usage: "deploy [dir] [--tenant <tailnet>] [--name <workload>] [--env <name>] [--wrangler | --from-wrangler-out <dir>] [--host <id>] [--replace] [--force-takeover] [--allow-missing-grants] [--no-wait] [--platform linux/amd64,linux/arm64] [--wait-public 5m]", help: "bundle, preflight grants, build + push Dockerfile container images (docker buildx), place (prints why), deploy, wait until ready; --replace re-places a running workload", run: cmdDeploy},
		{name: "ls", usage: "ls [--tenant <tailnet>] [--all] [--limit N]", help: "list deployments and objects (paged; --all follows every page and shows killed objects)", run: cmdLs},
		{name: "undeploy", usage: "undeploy <workload> [--tenant <tailnet>] [--reason <text>]", help: "stop a workload: kill its objects (cascade), release its lease", run: cmdUndeploy},
		{name: "expose", usage: "expose on|off <workload> [--tenant <tailnet>]", help: "open or close a workload's public (Funnel) routes; survives redeploys (twd.jsonc \"public\", ADR-029)", run: cmdExpose},
		{name: "secret", usage: "secret put <NAME> | secret ls | secret rm <NAME>   [--workload <workload>] [--tenant <tailnet>]", help: "a workload's secrets (env.NAME, like wrangler secret): put reads the value from stdin (hidden prompt on a terminal), ls shows names only; changes apply on the next deploy", run: cmdSecret},
		{name: "d1", usage: d1Usage, help: "a workload's D1 databases (like wrangler d1): create, list, delete, run SQL, export, and wrangler-layout migrations; execute/export/migrations reach the running workload", run: cmdD1},
		{name: "kv", usage: "kv namespace create <NAME> | kv namespace list | kv namespace delete <NAME> | kv key put <KEY> [VALUE] [--path FILE] [--ttl S] [--expiration T] [--metadata JSON] | kv key get <KEY> | kv key delete <KEY> | kv key list [--prefix P]   [--binding <BINDING> or --namespace <NAME>] [--workload <workload>] [--tenant <tailnet>]", help: "a workload's Workers KV namespaces (kv_namespaces, like wrangler kv): key commands name the namespace by --binding (from the wrangler config) or --namespace; writes reach running workloads within 60 s", run: cmdKV},
		{name: "queues", usage: "queues create <queue> | queues ls | queues info <queue> | queues rm <queue> | queues purge <queue> | queues messages <queue> [--limit N] | queues send <queue> [<json>|--text <text>] [--delay S]   [--tenant <tailnet>]", help: "the tenant's queues (like wrangler queues): producers' first send creates a queue; messages peeks without claiming (read a dead-letter queue); send takes JSON (or --text) from the argument or stdin", run: cmdQueues},
		{name: "r2", usage: r2Usage, help: "a workload's R2 buckets (r2_buckets bindings): list, create or delete buckets, list a bucket's objects", run: cmdR2},
		{name: "telemetry", usage: "telemetry [show] | telemetry set --otlp <url> [--header NAME[=VALUE]]... [--signals logs,metrics,traces] | telemetry rm   [--tenant <tailnet>]", help: "the tenant's OTLP export of request records, metrics and spans (each workload exports from its own node); --header NAME reads a credential from stdin, never shown again", run: cmdTelemetry},
		{name: "kill", usage: "kill <object> [--tenant <tailnet>] [--cascade] [--reason <text>]", help: "kill an object (and with --cascade its sub-agents)", run: cmdKill},
		{name: "approvals", usage: "approvals [workload] [--tenant <tailnet>] [--all]", help: "Workflow approvals waiting for a decision (with age and the instance's params)", run: cmdApprovals},
		{name: "approve", usage: "approve <workload> <instance> [--reject] [--comment <text>] [--workflow <name>] [--step <name>] [--tenant <tailnet>]", help: "approve (or --reject) a waiting Workflow as your own tailnet identity (needs a twd.dev/cap/approve grant; from a `twd link`ed machine)", run: cmdApprove},
		{name: "notify", usage: "notify [show] | notify set --webhook <url|-> [--header NAME[=VALUE]]... [--events approval_requested] | notify test | notify rm   [--tenant <tailnet>]", help: "tell people when a Workflow waits for an approval: mgmt POSTs JSON with a link to the approvals page (Slack and chat incoming webhooks show its text); the URL is a secret, never shown again", run: cmdNotify},
		{name: "audit", usage: "audit [--tenant <tailnet>] <object> | --human <login> | --trace <id> | --all", help: "show the hash-chained audit trail", run: cmdAudit},
		{name: "logs", usage: "logs [workload] [--tenant <tailnet>] [--component mgmt|hostd|supervisor] [--host <id>] [-n 50] [-f] [--json] [--console] [--object <id|Class/name>] [--status 5xx] [--outcome exception] [--errors] [--caller <login|node>] [--trace <id>]", help: "show a workload's request records (one line per request with its console.* lines; --json, filters; --console for the raw app output), else the daemons' logs", run: cmdLogs},
		{name: "doctor", usage: "doctor [--tailnet <tailnet> --api-key-file <file>]", help: "check this machine: workerd runs, bundler offline, ~/.twd writable, API key works, process sandbox active, system tailscaled not used; on Linux also glibc, kernel/Landlock ABI, KVM, firecracker, cgroups v2, systemd and the microVM tier", run: cmdDoctor},
		{name: "docs", usage: "docs search <words…> [--limit 10] | docs show <doc>[#<anchor>] | docs ls", help: "twd's docs, offline: search them, print one doc or section (e.g. errors.md#acl_denied)", run: cmdDocs},
		{name: "mcp", usage: "mcp [--allow deploy,kill,undeploy] | mcp --config", help: "twd's MCP server for coding agents, over stdio (a bridge to mgmt's /mcp; tools: whoami status ls logs audit, and deploy kill undeploy when allowed); --config prints how to register it", run: cmdMCP},
		{name: "agent", usage: "agent setup [--scope user|project] [--dir <dir>] [--agent claude,codex,cursor,gemini,opencode] [--dry-run] [--yes] | agent remove [same flags] | agent ls | agent show <skill>", help: "teach coding agents (Claude Code, Codex, Cursor, Gemini CLI, OpenCode) to drive twd: install or remove twd's skills, list or print them", run: cmdAgent},
		{name: "host", usage: "host add <host-id> [--tenant <tailnet>] [--out <file>] | host ls | host rm <host-id> [--drain] | host cordon <host-id> | host uncordon <host-id> | host drain <host-id> [--allow-local-state-loss] [--dry-run] | host setup --microvm [--user <name>] [--hugepages N] | host setup --undo   (Linux, with sudo, once) | host vmcheck <workload> [--keyscan] [--container <Class>/<name>]", help: "add a remote host (mgmt mints its single-use tag:twd-host join key), list or remove hosts; cordon (no new placements), uncordon, or drain one (move its workloads elsewhere); install the microVM tier's helper; check a microVM", run: cmdHost},
		{name: "image", usage: "image public add <ref> [--arch amd64,arm64] | image public ls | image public rm <id> | image push <oci-layout|docker-save.tar> twd.local/<name>:<tag>", help: "container images (ADR-032): mark public base images shared across tenants (mirrored now), push operator images (the arm64 alias targets) to twd's registry", run: cmdImage},
		{name: "upgrade", usage: upgradeUsage, help: "upgrade twd: on a twd up machine, back up, install a newer release and roll back if it isn't healthy; against a linked twd-mgmt (or --fleet), plan and run a fleet upgrade (status, pause, resume, abort)", run: cmdUpgrade},
		{name: "release", usage: releaseUsage, help: "twd-mgmt's release catalog: list known releases, import a release (its notes and tarball, for hosts that upgrade themselves)", run: cmdRelease},
		{name: "version", usage: "version", help: "print the twd version (and the pinned workerd / built-in esbuild)", run: cmdVersion},
		{name: "__supervise", hidden: true, run: cmdSupervise},
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "twd: Workers and Durable Objects with their own tailnet identities")
	fmt.Fprintln(w, "\nusage: twd <command> [flags]   (every command takes --state-dir, default ~/.twd, and --json)")
	fmt.Fprintln(w, "       twd <command> --help     usage of one command")
	fmt.Fprintln(w)
	for _, c := range commands {
		if !c.hidden {
			fmt.Fprintf(w, "  %-8s %s\n           twd %s\n", c.name, c.help, c.usage)
		}
	}
	fmt.Fprintln(w, "\ntemplates (twd init --template): hello | agent | mcp | ai-agent | lock | refund-approval | node-workflow (node-workflow needs Node.js on the deploying machine)")
}

// commandUsage prints one command's help (`twd <cmd> --help`): the usage
// line and, when the command's flag set is known (f != nil), every flag
// with its default.
func commandUsage(w io.Writer, c command, f *flags) {
	fmt.Fprintf(w, "twd %s: %s\n\nusage: twd %s\n", c.name, c.help, c.usage)
	if f == nil {
		fmt.Fprintln(w, "  (every command takes --state-dir, default ~/.twd, and --json)")
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
		// `twd help <cmd>` is `twd <cmd> --help`.
		if len(args) == 2 && args[0] == "help" {
			for _, c := range commands {
				if c.name == args[1] && !c.hidden {
					commandHelp(stdout, c, []string{"--help"})
					return 0
				}
			}
		}
		usage(stdout)
		return 0
	}
	// Allow global flags before the command: twd --state-dir X <cmd> …
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
//	  https://github.com/rosskukulinski/twd/blob/master/docs/errors.md#<code>
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
		fmt.Fprintln(os.Stderr, netd.Redact(string(b)))
		return
	}
	fmt.Fprintf(os.Stderr, "error [%s]: %s\n", e.Code, netd.Redact(e.Message))
	for _, l := range e.Lines {
		fmt.Fprintf(os.Stderr, "  %s\n", netd.Redact(l))
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
	f.stateDir = fs.String("state-dir", "", "state directory (default $TWD_STATE_DIR or ~/.twd)")
	f.json = fs.Bool("json", false, "machine-readable output")
	return f
}

// parse parses flags interleaved with positional args (twd kill X --cascade).
func (f *flags) parse(args []string) ([]string, error) {
	var pos []string
	for {
		if err := f.FlagSet.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, &helpRequest{f: f}
			}
			return nil, fail("bad_request", err.Error(), "usage: twd "+usageOf(f.Name()))
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
		fmt.Println(netd.Redact(string(b)))
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
