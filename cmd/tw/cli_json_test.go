package main_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/agenteval"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"github.com/rosskukulinski/tailworks/schema"
)

// cliSurface is every tailworks command and subcommand, with arguments that run
// hermetically against an empty state dir and a fake home, and the
// schema/cli.schema.json $def of its result. Most fail there (not_up,
// bad_request, ...), which checks the error contract; TestAgentEval checks
// the results of the ones that need a running tailworks.
var cliSurface = []struct {
	def  string
	args []string
}{
	{"version", []string{"version"}},
	{"status", []string{"status"}},
	{"up", []string{"up", "--yes"}},
	{"down", []string{"down"}},
	{"link", []string{"link", "--unlink"}},
	{"whoami", []string{"whoami"}},
	{"tenant add", []string{"tenant", "add", "other.example.ts.net", "--oauth-client-file", "/nonexistent/oauth.env"}},
	{"tenant ls", []string{"tenant", "ls"}},
	{"tenant set", []string{"tenant", "set", "other.example.ts.net", "--max-objects", "5"}},
	{"tenant rm", []string{"tenant", "rm", "other.example.ts.net", "--yes"}},
	{"init", []string{"init", "{work}/app", "--template", "hello"}},
	{"init list", []string{"init", "--list"}},
	{"dev", []string{"dev", "{work}/empty", "--once"}},
	{"types", []string{"types", "{work}/app"}},
	{"deploy", []string{"deploy", "{work}/app"}},
	{"deployments", []string{"deployments", "app"}},
	{"policy", []string{"policy", "{work}/app"}},
	{"previews", []string{"previews", "app"}},
	{"promote", []string{"promote", "app", "3f9a1c2b"}},
	{"rollback", []string{"rollback", "app"}},
	{"ls", []string{"ls"}},
	{"undeploy", []string{"undeploy", "app"}},
	{"expose", []string{"expose", "off", "app"}},
	{"expose names", []string{"expose", "names"}},
	{"expose release", []string{"expose", "release", "app"}},
	{"secret put", []string{"secret", "put", "API_TOKEN", "--workload", "app"}},
	{"secret bulk", []string{"secret", "bulk", "/nonexistent/secrets.env", "--workload", "app"}},
	{"secret ls", []string{"secret", "ls", "--workload", "app"}},
	{"secret rm", []string{"secret", "rm", "API_TOKEN", "--workload", "app"}},
	{"kv namespace create", []string{"kv", "namespace", "create", "sessions", "--workload", "app"}},
	{"kv namespace list", []string{"kv", "namespace", "ls", "--workload", "app"}},
	{"kv namespace delete", []string{"kv", "namespace", "rm", "sessions", "--workload", "app", "--yes"}},
	{"kv key put", []string{"kv", "key", "put", "k", "v", "--namespace", "sessions", "--workload", "app"}},
	{"kv key get", []string{"kv", "key", "get", "k", "--namespace", "sessions", "--workload", "app"}},
	{"kv key delete", []string{"kv", "key", "rm", "k", "--namespace", "sessions", "--workload", "app"}},
	{"kv key list", []string{"kv", "key", "ls", "--namespace", "sessions", "--workload", "app"}},
	{"queues create", []string{"queues", "create", "jobs"}},
	{"queues ls", []string{"queues", "ls"}},
	{"queues info", []string{"queues", "info", "jobs"}},
	{"queues rm", []string{"queues", "rm", "jobs", "--yes"}},
	{"queues purge", []string{"queues", "purge", "jobs", "--yes"}},
	{"queues messages", []string{"queues", "messages", "jobs"}},
	{"queues send", []string{"queues", "send", "jobs", `{"k":1}`}},
	{"kill", []string{"kill", "app"}},
	{"approvals", []string{"approvals"}},
	{"approve", []string{"approve", "app", "wf_1"}},
	{"notify", []string{"notify"}},
	{"notify", []string{"notify", "set", "--webhook", "https://hooks.example.com/x"}},
	{"notify", []string{"notify", "test"}},
	{"notify", []string{"notify", "rm"}},
	{"audit", []string{"audit", "--all"}},
	{"logs.line", []string{"logs", "app"}},
	{"doctor", []string{"doctor"}},
	{"telemetry", []string{"telemetry"}},
	{"telemetry", []string{"telemetry", "set", "--otlp", "http://otel-collector:4318"}},
	{"telemetry", []string{"telemetry", "rm"}},
	{"mcp", []string{"mcp", "--config"}},
	{"docs ls", []string{"docs", "ls"}},
	{"docs show", []string{"docs", "show", "errors.md#bad_request"}},
	{"docs search", []string{"docs", "search", "tagOwners"}},
	{"completion", []string{"completion", "bash"}},
	{"agent setup", []string{"agent", "setup", "--dry-run"}},
	{"agent remove", []string{"agent", "remove", "--dry-run"}},
	{"agent ls", []string{"agent", "ls"}},
	{"agent show", []string{"agent", "show", "tw"}},
	{"host add", []string{"host", "add", "h1"}},
	{"host ls", []string{"host", "ls"}},
	{"host rm", []string{"host", "rm", "h1", "--yes"}},
	{"host cordon", []string{"host", "cordon", "h1"}},
	{"host uncordon", []string{"host", "uncordon", "h1"}},
	{"host drain", []string{"host", "drain", "h1", "--dry-run"}},
	{"uninstall", []string{"uninstall", "--dry-run"}},
	{"backup", []string{"backup"}},
	{"restore", []string{"restore", "/nonexistent/backup", "--yes"}},
	{"custody migrate", []string{"custody", "migrate", "--setec-url", "https://secrets.example.ts.net"}},
	{"upgrade", []string{"upgrade", "--plan", "--from", "/nonexistent"}},
	{"upgrade", []string{"upgrade", "fleet", "--plan"}},
	{"upgrade", []string{"upgrade", "fleet", "status"}},
	{"upgrade import", []string{"upgrade", "import", "/nonexistent/tailworks-0.0.0-linux-amd64.tar.gz"}},
	{"upgrade releases", []string{"upgrade", "releases"}},
	{"upgrade rollback", []string{"upgrade", "rollback", "--yes"}},
	{"host setup", []string{"host", "setup"}},
	{"host vmcheck", []string{"host", "vmcheck", "app"}},
	{"image public add", []string{"image", "public", "add", "docker.io/library/alpine:3"}},
	{"image public ls", []string{"image", "public", "ls"}},
	{"image public rm", []string{"image", "public", "rm", "x"}},
	{"image push", []string{"image", "push", "/nonexistent/layout", "tw.local/x:1"}},
}

// TestCLIJSONContract walks every command and subcommand with --json and
// checks the output contract (docs/cli-json.md): a success is one JSON
// document on stdout matching the command's schema; a failure is
// {"error": {code, message, hint, doc_url}} on stderr with a code from
// docs/errors.md, and nothing on stdout. It also fails when a command or
// subcommand has no schema, or the schema describes one that's gone.
func TestCLIJSONContract(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, "empty"), 0o755)
	home := filepath.Join(work, "home")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	state, _ := hostdtest.ShortDir("tw-js-")
	defer os.RemoveAll(state)
	ag := &agenteval.Agent{T: t, Bin: filepath.Join(bin, "tw"), StateDir: state, Dir: work,
		Env: []string{"HOME=" + home, "TW_STATE_DIR=" + state}}

	// Every command and subcommand in `tw help` is in cliSurface.
	hb, err := exec.Command(filepath.Join(bin, "tw"), "help", "--all").Output()
	if err != nil {
		t.Fatal(err)
	}
	help := string(hb)
	have := map[string]bool{}
	for _, c := range cliSurface {
		have[strings.Join(c.args[:min(len(c.args), subDepth(c.args))], " ")] = true
	}
	for _, c := range helpCommands(help) {
		if !have[c] {
			t.Errorf("tw help lists %q but cliSurface (and so the --json contract test) doesn't", c)
		}
	}
	// … and the schema has a def for each, and no def for a command that's gone.
	defs := map[string]bool{}
	for _, d := range schema.CLIDefs() {
		defs[d] = true
	}
	used := map[string]bool{}
	for _, c := range cliSurface {
		if !defs[c.def] {
			t.Errorf("schema/cli.schema.json has no $defs/%q", c.def)
		}
		used[c.def] = true
	}
	for _, d := range schema.CLIDefs() {
		cmdDef := regexp.MustCompile(`^[a-z]+( [a-z]+)*$`).MatchString(d) && !map[string]bool{"error": true, "object": true, "deployment": true, "skill": true, "secret": true, "timings": true}[d]
		if cmdDef && !used[d] {
			t.Errorf("schema/cli.schema.json $defs/%q: no such command in cliSurface", d)
		}
	}
	for _, d := range []string{"dev.event", "deploy.event", "logs.line", "logs.record", "error"} {
		if !defs[d] {
			t.Errorf("schema/cli.schema.json has no $defs/%q", d)
		}
	}

	var oks []string
	for _, c := range cliSurface {
		args := make([]string, 0, len(c.args)+1)
		for _, a := range c.args {
			args = append(args, strings.ReplaceAll(a, "{work}", work))
		}
		r := ag.Exec(append(args, "--json")...)
		switch {
		case c.def == "doctor":
			// The report is printed whether or not every check passes.
			v, err := agenteval.Doc(r)
			if err == nil {
				err = agenteval.CheckDoc("doctor", v)
			}
			if err != nil {
				t.Errorf("tw doctor --json: %v", err)
			}
			if !r.OK() {
				if _, err := agenteval.CheckError(agenteval.Run{Args: r.Args, Stderr: r.Stderr, Exit: r.Exit}); err != nil {
					t.Error(err)
				}
			}
		case c.def == "logs.line" && r.OK():
			if _, err := agenteval.CheckLogs(r.Stdout); err != nil {
				t.Error(err)
			}
		case r.OK():
			oks = append(oks, c.def)
			if err := agenteval.CheckResult(c.def, r); err != nil {
				t.Errorf("tw %s: %v", strings.Join(args, " "), err)
			}
		default:
			if _, err := agenteval.CheckError(r); err != nil {
				t.Errorf("%v", err)
			}
		}
	}
	sort.Strings(oks)
	t.Logf("succeeded hermetically (result checked): %s", strings.Join(oks, ", "))

	// Usage errors and unknown commands are JSON too.
	for _, args := range [][]string{{"no-such-command"}, {"deploy", "--no-such-flag"}, {"agent"}, {"docs", "show"}} {
		r := ag.Exec(append(args, "--json")...)
		e, err := agenteval.CheckError(r)
		if err != nil {
			t.Fatal(err)
		}
		if e["code"] != "bad_request" {
			t.Errorf("tw %s --json: code %v, want bad_request", strings.Join(args, " "), e["code"])
		}
	}
	// --json without --yes never prompts: agent setup refuses to write.
	r := ag.Exec("agent", "setup", "--json")
	if e, err := agenteval.CheckError(r); err != nil || e["code"] != "confirmation_required" {
		t.Fatalf("agent setup --json without --yes: %v %v", e, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); err == nil {
		t.Fatal("agent setup wrote files without --yes")
	}
}

// subDepth is how many words name the command: 3 for image public <x>, 2
// for the commands with subcommands, else 1.
func subDepth(args []string) int {
	switch args[0] {
	case "image":
		if len(args) > 1 && args[1] == "public" {
			return 3
		}
		return 2
	case "tenant", "host", "secret", "docs", "agent", "queues":
		return 2
	case "kv":
		return 3 // kv namespace create, kv key put, …
	}
	return 1
}

// helpCommands parses `tw help`: every command, and each subcommand of the
// commands whose usage lists them (one "tw tenant add …" line per
// subcommand, or "tenant add … | tenant ls | …").
func helpCommands(help string) []string {
	sc := bufio.NewScanner(strings.NewReader(help))
	cmdLine := regexp.MustCompile(`^  ([a-z]+) `)
	subs := map[string]map[string]bool{} // command → its subcommands
	var cur string
	for sc.Scan() {
		l := sc.Text()
		if m := cmdLine.FindStringSubmatch(l); m != nil {
			cur = m[1]
			subs[cur] = map[string]bool{}
			continue
		}
		u, ok := strings.CutPrefix(strings.TrimSpace(l), "tw ")
		if !ok || cur == "" {
			continue
		}
		for _, seg := range strings.Split(u, "|") {
			w := strings.Fields(seg)
			if len(w) < 2 || w[0] != cur || subDepth(w) == 1 {
				continue
			}
			subs[cur][strings.Join(w[:subDepth(w)], " ")] = true
		}
	}
	var out []string
	for c, s := range subs {
		if len(s) == 0 {
			out = append(out, c)
		}
		for sub := range s {
			out = append(out, sub)
		}
	}
	sort.Strings(out)
	return out
}

// TestDevJSONEvents: `tw dev --json` (no --once) is an NDJSON stream on
// stdout: ready, reload after an edit, stopping on SIGINT; every line
// matches $defs/dev.event.
func TestDevJSONEvents(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "hello")
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	e.MustJSON(nil, "init", app, "--template", "hello")
	cmd := exec.Command(filepath.Join(bin, "tw"), "dev", app, "--port", "0", "--json")
	cmd.Env = os.Environ()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("tw dev --json: not an NDJSON line: %q", sc.Text())
				continue
			}
			if err := agenteval.CheckDoc("dev.event", m); err != nil {
				t.Error(err)
			}
			lines <- m
		}
		close(lines)
	}()
	next := func(want string) map[string]any {
		t.Helper()
		select {
		case m, ok := <-lines:
			if !ok || m["event"] != want {
				t.Fatalf("tw dev --json: want a %q event, got %v", want, m)
			}
			return m
		case <-time.After(90 * time.Second):
			t.Fatalf("tw dev --json: no %q event", want)
		}
		return nil
	}
	ready := next("ready")
	if u, _ := ready["url"].(string); !strings.HasPrefix(u, "http://127.0.0.1:") || ready["workload"] != "hello" {
		t.Fatalf("ready event: %v", ready)
	}
	if u, _ := next("console")["url"].(string); !strings.HasPrefix(u, "http://127.0.0.1:") { // devconsole.go
		t.Fatalf("console event url %q", u)
	}
	src := filepath.Join(app, "src", "index.ts")
	b, _ := os.ReadFile(src)
	os.WriteFile(src, append(b, []byte("\n// edited\n")...), 0o644)
	next("reload")
	cmd.Process.Signal(syscall.SIGINT)
	next("stopping")
	cmd.Wait()
}
