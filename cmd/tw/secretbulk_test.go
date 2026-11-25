package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

const bulkSecret = "sk-bulk-0123456789"

// TestReadSecretBulk: dotenv and JSON (sniffed by the first character),
// in input order; every refusal names the line or the name, never a value.
func TestReadSecretBulk(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	names := func(vs []dotenvVar) string {
		var out []string
		for _, v := range vs {
			out = append(out, v.name+"="+v.value)
		}
		return strings.Join(out, ";")
	}
	for _, c := range []struct{ in, want string }{
		{"# c\nB=" + bulkSecret + "\nexport A=\"x y\"\n", "B=" + bulkSecret + ";A=x y"},
		{"\xef\xbb\xbf \n{\"B\": \"" + bulkSecret + "\", \"A\": \"\"}\n", "B=" + bulkSecret + ";A="},
	} {
		vs, err := readSecretBulk(write("in", c.in))
		if err != nil || names(vs) != c.want {
			t.Errorf("%q: %v %q", c.in, err, names(vs))
		}
	}
	// stdin, piped; "-" too.
	withSecretInput(t, `{"K":"`+bulkSecret+`"}`, false)
	if vs, err := readSecretBulk(""); err != nil || names(vs) != "K="+bulkSecret {
		t.Errorf("stdin: %v %q", err, names(vs))
	}
	withSecretInput(t, "K="+bulkSecret, false)
	if vs, err := readSecretBulk("-"); err != nil || names(vs) != "K="+bulkSecret {
		t.Errorf("stdin -: %v %q", err, names(vs))
	}
	// A terminal on stdin: the fix, not a hang.
	withSecretInput(t, "", true)
	if _, err := readSecretBulk(""); cliCode(err) != "bad_request" || !strings.Contains(cliText(err), "tw secret bulk .dev.vars") {
		t.Errorf("tty: %v", err)
	}

	big := strings.Repeat("v", mgmt.SecretMaxValueBytes+1)
	for _, c := range []struct{ in, want string }{
		{"A=1\nA=" + bulkSecret + "\n", "line 2: A is given twice (first on line 1)"},
		{`{"A": "1", "A": "` + bulkSecret + `"}`, "A is given twice"},
		{"A=1\n" + bulkSecret + "\n", "line 2"},
		{"A=1\n" + bulkSecret + "=x\n", "line 2"},
		{"TW_X=" + bulkSecret, "line 1"},
		{`{"A": 1, "B": "` + bulkSecret + `"}`, "the value of A must be a JSON string"},
		{`{"A": ["` + bulkSecret + `"]}`, "the value of A must be a JSON string"},
		{`{"` + bulkSecret + `": "x"}`, "line 1: the secret name"},
		{"{\n\"A\": \"" + bulkSecret + "\",\n}", "not a JSON object"},
		{`{"A": "` + bulkSecret + `"} trailing`, "not a JSON object"},
		{`{"A": "` + bulkSecret, "not a JSON object"},
		{`B="` + bulkSecret, "unterminated"},
		{"B=" + big, "the value of B is over"},
		{`{"B": "` + big + `"}`, "the value of B is over"},
		{"# only a comment\n", "no secrets"},
		{"", "no secrets"},
		{"A=\xff" + bulkSecret, "not UTF-8"},
	} {
		_, err := readSecretBulk(write("bad", c.in))
		text := cliText(err)
		b, _ := json.Marshal(asCLIErr(err))
		if cliCode(err) != "bad_request" || !strings.Contains(text, c.want) {
			t.Errorf("%.40q: %v, want %q", c.in, err, c.want)
		}
		if strings.Contains(text+string(b), bulkSecret) || strings.Contains(text, "vvvvvvvv") {
			t.Errorf("%.40q: the error echoes a value: %s", c.in, text)
		}
	}
	// A missing file: the OS error, not the argument (it may be a value).
	_, err := readSecretBulk(filepath.Join(dir, bulkSecret))
	if cliCode(err) != "bad_request" || strings.Contains(cliText(err), bulkSecret) {
		t.Errorf("missing file: %v", err)
	}
}

// TestSecretBulkArgs: NAME=VALUE and extra arguments are refused before
// anything is read, and never echoed, flags before the verb or not.
func TestSecretBulkArgs(t *testing.T) {
	t.Chdir(t.TempDir())
	withSecretInput(t, "A=1", false)
	for _, args := range [][]string{
		{"bulk", "API_KEY=" + bulkSecret},
		{"--workload", "app", "bulk", "API_KEY=" + bulkSecret},
		{"bulk", "f.env", bulkSecret},
		{"--tenant", "t.example.ts.net", "bulk", bulkSecret, "x"},
	} {
		err := cmdSecret(append(args, "--state-dir", t.TempDir()))
		b, _ := json.Marshal(asCLIErr(err))
		if cliCode(err) != "bad_request" || strings.Contains(cliText(err)+string(b), bulkSecret) {
			t.Errorf("%v: %v", args[:2], err)
		}
	}
}

// TestPutSecretsInTurn: a failure stops the run and names what was set,
// what failed and what was not attempted, with the cause's code.
func TestPutSecretsInTurn(t *testing.T) {
	vars := []dotenvVar{{name: "A", value: bulkSecret}, {name: "B", value: bulkSecret}, {name: "C", value: bulkSecret}, {name: "D"}}
	var shown []string
	set, failed, not, err := putSecretsInTurn(vars, func(v dotenvVar) (bool, error) {
		if v.name == "B" {
			return false, fail("too_many_secrets", "workload w already has 128 secrets (at most 128)")
		}
		return true, nil
	}, func(name string, _ bool) { shown = append(shown, name) })
	text := cliText(err)
	if cliCode(err) != "too_many_secrets" || strings.Join(set, ",") != "A" || strings.Join(failed, ",") != "B" || strings.Join(not, ",") != "C,D" ||
		!strings.Contains(text, "stopped at B") || !strings.Contains(text, "set: A") || !strings.Contains(text, "not attempted: C, D") || strings.Join(shown, ",") != "A" {
		t.Fatalf("%v %v %v %v\n%s", set, failed, not, err, text)
	}
	if strings.Contains(text, bulkSecret) {
		t.Fatal("the error carries a value")
	}
	set, failed, not, err = putSecretsInTurn(vars, func(dotenvVar) (bool, error) { return false, nil }, func(string, bool) {})
	if err != nil || len(set) != 4 || failed == nil || len(failed) != 0 || not == nil || len(not) != 0 {
		t.Fatalf("all: %v %v %v %v", set, failed, not, err)
	}
	if _, _, _, err := putSecretsInTurn(vars[:1], func(dotenvVar) (bool, error) { return false, errors.New("boom") }, func(string, bool) {}); cliCode(err) != "internal" {
		t.Fatalf("plain error: %v", err)
	}
}

// TestCLISecretBulk: a dotenv file and piped JSON reach mgmt intact, in
// order, with only names in the output, the --json lists and the audit;
// names not in the input are kept.
func TestCLISecretBulk(t *testing.T) {
	e := newPubEnv(t)
	e.setPolicy(`{}`)
	app := writePlainApp(t)
	if out, err := e.call(t, cmdDeploy, app, "--allow-missing-grants", "--no-wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	ctx := context.Background()
	noValue := func(what, s string) {
		t.Helper()
		if strings.Contains(s, bulkSecret) {
			t.Fatalf("%s carries a secret value:\n%s", what, s)
		}
	}
	withSecretInput(t, "kept", false)
	if _, err := e.call(t, cmdSecret, "put", "KEPT", "--workload", "support"); err != nil {
		t.Fatal(err)
	}

	f := filepath.Join(t.TempDir(), ".dev.vars")
	os.WriteFile(f, []byte("API_KEY="+bulkSecret+"\nOTHER=\"two\\nlines\"\n"), 0o600)
	out, err := e.call(t, cmdSecret, "bulk", f, "--workload", "support")
	if err != nil || !strings.Contains(out, "secret API_KEY created") || !strings.Contains(out, "2 secrets set for workload support") || !strings.Contains(out, "kept") {
		t.Fatalf("bulk file: %v\n%s", err, out)
	}
	noValue("bulk output", out)

	t.Chdir(app)
	withSecretInput(t, `{"OTHER": "`+bulkSecret+`-2", "THIRD": "3"}`, false)
	out, err = e.call(t, cmdSecret, "bulk", "--json")
	var r secretBulkResult
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || strings.Join(r.Set, ",") != "OTHER,THIRD" || r.Workload != "support" || len(r.Failed) != 0 || r.NotAttempted == nil {
		t.Fatalf("bulk stdin --json: %v\n%s", err, out)
	}
	noValue("bulk --json", out)

	l, err := e.st.GetLease(ctx, mgmt.LeaseName(e.tenantID, "support"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := e.m.ReleaseSecrets(ctx, e.tenantID, "support", l.Holder, l.Epoch)
	if err != nil || vals["API_KEY"] != bulkSecret || vals["OTHER"] != bulkSecret+"-2" || vals["THIRD"] != "3" || vals["KEPT"] != "kept" || len(vals) != 4 {
		t.Fatalf("release: %v (%d values)", err, len(vals))
	}
	recs, _ := e.st.ListAudit(ctx, mgmt.AuditFilter{TenantID: e.tenantID, Kinds: []string{"secret_put"}})
	if len(recs) != 5 {
		t.Fatalf("audit records %d", len(recs))
	}
	for _, r := range recs {
		noValue("audit "+r.Kind, describe(r, nil)+string(r.Detail))
	}
}
