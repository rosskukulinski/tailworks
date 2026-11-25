package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

const cliSecret = "sk-cli-0123456789"

// withSecretInput makes `tw secret put` read value (tty: as if typed at
// the hidden prompt) for the rest of the test.
func withSecretInput(t *testing.T, value string, tty bool) {
	t.Helper()
	old := secretInput
	t.Cleanup(func() { secretInput = old })
	secretInput = func() (io.Reader, bool, func() ([]byte, error)) {
		return strings.NewReader(value), tty, func() ([]byte, error) {
			if !tty {
				return nil, errors.New("not a terminal")
			}
			return []byte(value), nil
		}
	}
}

func writePlainApp(t *testing.T) string {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "src"), 0o755)
	os.WriteFile(filepath.Join(d, "tailworks.jsonc"), []byte(`{ "name": "support", "tailnet": { "tags": ["tag:tw-wl-support"] } }`), 0o644)
	os.WriteFile(filepath.Join(d, "wrangler.jsonc"), []byte(`{"name": "support", "main": "src/index.js", "compatibility_date": "2026-04-01"}`), 0o644)
	os.WriteFile(filepath.Join(d, "src", "index.js"), []byte(`export default { fetch(req, env) { return new Response(typeof env.API_KEY); } };`), 0o644)
	return d
}

// TestCLISecret: put (stdin, trailing newline dropped; and the hidden
// prompt), ls (names only), rm, the workload taken from the app in the
// cwd, and the value never in argv, output or errors. The value reaches
// mgmt's lease-fenced grant intact.
func TestCLISecret(t *testing.T) {
	e := newPubEnv(t)
	e.setPolicy(`{}`)
	app := writePlainApp(t)
	if out, err := e.call(t, cmdDeploy, app, "--allow-missing-grants", "--no-wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	ctx := context.Background()
	noValue := func(what, s string) {
		t.Helper()
		if strings.Contains(s, cliSecret) {
			t.Fatalf("%s carries the secret value:\n%s", what, s)
		}
	}

	// From stdin (a pipe), with --workload.
	withSecretInput(t, cliSecret+"\n", false)
	out, err := e.call(t, cmdSecret, "put", "API_KEY", "--workload", "support")
	if err != nil || !strings.Contains(out, "secret API_KEY created for workload support") || !strings.Contains(out, "tw deploy") {
		t.Fatalf("put: %v\n%s", err, out)
	}
	noValue("put output", out)

	// From the app in the cwd, at the hidden prompt.
	t.Chdir(app)
	withSecretInput(t, "typed-value", true)
	if out, err := e.call(t, cmdSecret, "put", "OTHER"); err != nil || !strings.Contains(out, "OTHER created for workload support") {
		t.Fatalf("put from cwd: %v\n%s", err, out)
	}
	withSecretInput(t, cliSecret, false)
	if out, err := e.call(t, cmdSecret, "put", "API_KEY", "--json"); err != nil || !strings.Contains(out, `"workload": "support"`) {
		t.Fatalf("put again: %v\n%s", err, out)
	} else {
		noValue("put --json", out)
	}

	// The value is never taken from the command line, and never echoed.
	for _, args := range [][]string{{"put", "API_KEY", cliSecret}, {"put", cliSecret}, {"put", "API_KEY", "--value=" + cliSecret}, {"put", "API_KEY", "--value", cliSecret}} {
		out, err := e.call(t, cmdSecret, args...)
		if code := cliCode(err); code != "bad_request" {
			t.Fatalf("%v: %v", args[:2], err)
		}
		b, _ := json.Marshal(asCLIErr(err))
		noValue("error", cliText(err)+string(b)+out)
	}

	// ls: names and times only.
	out, err = e.call(t, cmdSecret, "ls")
	if err != nil || !strings.Contains(out, "API_KEY") || !strings.Contains(out, "OTHER") {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	noValue("ls", out)
	out, err = e.call(t, cmdSecret, "ls", "--json")
	var lr secretResult
	if err != nil || json.Unmarshal([]byte(out), &lr) != nil || len(lr.Secrets) != 2 || lr.Secrets[0].Name != "API_KEY" {
		t.Fatalf("ls --json: %v\n%s", err, out)
	}
	noValue("ls --json", out)
	if strings.Contains(out, "typed-value") {
		t.Fatal("ls --json carries a value")
	}

	// The values in mgmt: only the lease holder gets them, intact.
	l, err := e.st.GetLease(ctx, mgmt.LeaseName(e.tenantID, "support"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := e.m.ReleaseSecrets(ctx, e.tenantID, "support", l.Holder, l.Epoch)
	if err != nil || vals["API_KEY"] != cliSecret || vals["OTHER"] != "typed-value" {
		t.Fatalf("release: %v (%d values)", err, len(vals))
	}

	// rm.
	if out, err := e.call(t, cmdSecret, "rm", "OTHER"); err != nil || !strings.Contains(out, "OTHER deleted") {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdSecret, "rm", "OTHER"); cliCode(err) != "unknown_secret" {
		t.Fatalf("rm again: %v", err)
	}
	out, _ = e.call(t, cmdSecret, "ls", "--workload", "other")
	if !strings.Contains(out, "no secrets for workload other") {
		t.Fatalf("ls other: %s", out)
	}
	// The audit renders the records by name.
	recs, _ := e.st.ListAudit(ctx, mgmt.AuditFilter{TenantID: e.tenantID, Kinds: []string{"secret_put", "secret_delete", "secret_release"}})
	if len(recs) != 5 {
		t.Fatalf("audit records %d", len(recs))
	}
	for _, r := range recs {
		d := describe(r, nil)
		noValue("audit "+r.Kind, d+string(r.Detail))
		if !strings.Contains(d, "support") {
			t.Fatalf("describe %s: %q", r.Kind, d)
		}
	}
}

func TestCLISecretNoApp(t *testing.T) {
	t.Chdir(t.TempDir())
	withSecretInput(t, "x", false)
	err := cmdSecret([]string{"--state-dir", t.TempDir(), "put", "A"})
	if cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "--workload") {
		t.Fatalf("no app: %v", err)
	}
	if err := cmdSecret([]string{"ls", "extra"}); cliCode(err) != "bad_request" {
		t.Fatalf("ls extra: %v", err)
	}
	if err := cmdSecret([]string{"frob"}); cliCode(err) != "bad_request" {
		t.Fatalf("frob: %v", err)
	}
}

func TestParseDotenv(t *testing.T) {
	t.Parallel()
	got, err := parseDotenv(DevVarsFile, "# comment\n\nA=1\nexport B = two words # trailing\nC=\"line1\\nline2 \\\"q\\\"\"\nD='lit\\n $x'\nE=\"multi\nline\"\nF=\nG=a=b\r\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "line1\nline2 \"q\"", "D": `lit\n $x`, "E": "multi\nline", "F": "", "G": "a=b"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, bad := range []string{"NOEQUALS", "TW_X=1", "1A=x", `A="open`, "A='open"} {
		_, err := parseDotenv(DevVarsFile, bad+"\n")
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), "open") {
			t.Errorf("%q: error echoes the value: %v", bad, err)
		}
	}
}
