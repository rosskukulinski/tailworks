package main

// `tw secret bulk [file]`, like `wrangler secret bulk`: set several of a
// workload's secrets at once from a dotenv file (.dev.vars) or a JSON
// object of strings, read from the file or from piped stdin, never from
// the command line (ADR-034 §6). Every name, value size and the count are
// checked before the first PUT; then each secret is PUT in turn through
// the same route as `tw secret put` (audited by name and actor only). It
// only adds and replaces: a secret absent from the input is kept.
// No value, and no decoder message that could quote one, is ever printed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// secretBulkMaxBytes bounds the input: a workload's every secret at the
// largest size, with room for the syntax.
const secretBulkMaxBytes = 2 * mgmt.SecretMaxPerWorkload * mgmt.SecretMaxValueBytes

// secretBulkResult is `tw secret bulk --json`: the names set, the one that
// failed and those not attempted after it (empty on success). Never values.
type secretBulkResult struct {
	Workload     string   `json:"workload"`
	TenantID     string   `json:"tenantID"`
	Set          []string `json:"set"`
	Failed       []string `json:"failed"`
	NotAttempted []string `json:"notAttempted"`
}

var secretBulkFix = []string{
	"tw secret bulk .dev.vars              (NAME=VALUE lines)",
	"tw secret bulk secrets.json           ({\"NAME\": \"value\", …})",
	"cat secrets.json | tw secret bulk      (piped stdin)",
}

func cmdSecretBulk(f *flags, tenant, workloadFlag string, pos []string) error {
	if len(pos) > 1 {
		// Not echoed: a value may be among them.
		return fail("bad_request", "tw secret bulk takes one file (or none, to read piped stdin); values never go on the command line",
			"usage: tw "+usageOf("secret")).withFix(secretBulkFix...)
	}
	file := ""
	if len(pos) == 1 {
		file = pos[0]
	}
	if strings.Contains(file, "=") {
		return fail("bad_request", "tw secret bulk takes a file of secrets, not NAME=VALUE: values never go on the command line").withFix(secretBulkFix...)
	}
	vars, err := readSecretBulk(file)
	if err != nil {
		return err
	}
	workload, err := secretWorkload(workloadFlag, "bulk")
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(tenant)
	if err != nil {
		return err
	}
	base := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/secrets"
	// The count, before the first PUT: the workload's secrets after this.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cur, err := listAll[mgmtapi.SecretMeta](ctx, s.mgmt(), base, 0)
	cancel()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, m := range cur {
		have[m.Name] = true
	}
	total := len(cur)
	for _, v := range vars {
		if !have[v.name] {
			total++
		}
	}
	if total > mgmt.SecretMaxPerWorkload {
		return fail("bad_request", fmt.Sprintf("workload %s would have %d secrets (at most %d); nothing was set", workload, total, mgmt.SecretMaxPerWorkload)).
			withFix("tw secret ls     (and tw secret rm <NAME> the ones no longer used)")
	}

	hdr := map[string]string{"Tw-Actor": actor()}
	res := secretBulkResult{Workload: workload, TenantID: tn.TenantID}
	put := func(v dotenvVar) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var meta mgmtapi.SecretMeta
		if err := s.mgmt().DoHeaders(ctx, "PUT", base+"/"+v.name, map[string]string{"value": v.value}, &meta, hdr); err != nil {
			return false, err
		}
		return meta.CreatedAt == meta.UpdatedAt, nil
	}
	progress := func(name string, created bool) {
		if *f.json {
			return
		}
		verb := "updated"
		if created {
			verb = "created"
		}
		fmt.Printf("✓ secret %s %s\n", name, verb)
	}
	res.Set, res.Failed, res.NotAttempted, err = putSecretsInTurn(vars, put, progress)
	if err != nil {
		return err
	}
	emit(f, res, func() {
		fmt.Printf("%d secrets set for workload %s (tenant %s); secrets not in the input are kept (tw secret rm <NAME> removes one)\n", len(res.Set), workload, tn.Tailnet)
		fmt.Printf("  env.NAME in the workload from its next deploy: tw deploy\n")
	})
	return nil
}

// putSecretsInTurn PUTs vars in order and stops at the first failure. The
// error then names what was set, what failed and what was not attempted.
func putSecretsInTurn(vars []dotenvVar, put func(dotenvVar) (created bool, err error), progress func(name string, created bool)) (set, failed, notAttempted []string, err error) {
	set, failed, notAttempted = []string{}, []string{}, []string{}
	for i, v := range vars {
		created, err := put(v)
		if err != nil {
			failed = append(failed, v.name)
			for _, w := range vars[i+1:] {
				notAttempted = append(notAttempted, w.name)
			}
			e := *asCLIErr(err)
			e.Message = fmt.Sprintf("tw secret bulk stopped at %s: %s", v.name, e.Message)
			e.Lines = append(append([]string{}, e.Lines...),
				"set: "+namesOrNone(set),
				"failed: "+v.name,
				"not attempted: "+namesOrNone(notAttempted))
			return set, failed, notAttempted, &e
		}
		set = append(set, v.name)
		progress(v.name, created)
	}
	return set, failed, notAttempted, nil
}

func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// readSecretBulk reads and checks tw secret bulk's input: file, or piped
// stdin when file is "" or "-". JSON when its first character is {, else
// dotenv. Errors name the line and the name, never a value.
func readSecretBulk(file string) ([]dotenvVar, error) {
	label := file
	var b []byte
	if file == "" || file == "-" {
		label = "stdin"
		r, tty, _ := secretInput()
		if tty {
			return nil, fail("bad_request", "tw secret bulk needs a file, or secrets piped to stdin; stdin is a terminal").withFix(secretBulkFix...)
		}
		var err error
		if b, err = io.ReadAll(io.LimitReader(r, secretBulkMaxBytes+1)); err != nil {
			return nil, fail("bad_request", "can't read stdin: "+err.Error())
		}
	} else {
		fh, err := os.Open(file)
		if err != nil {
			// The OS error without the path: a mistyped value may be the path.
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err
			}
			return nil, fail("bad_request", "can't read the secrets file: "+err.Error()).withFix(secretBulkFix...)
		}
		b, err = io.ReadAll(io.LimitReader(fh, secretBulkMaxBytes+1))
		fh.Close()
		if err != nil {
			return nil, fail("bad_request", "can't read the secrets file")
		}
	}
	if len(b) > secretBulkMaxBytes {
		return nil, fail("bad_request", fmt.Sprintf("%s is over %d bytes", label, secretBulkMaxBytes))
	}
	if !utf8.Valid(b) {
		return nil, fail("bad_request", label+" is not UTF-8 text")
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var vars []dotenvVar
	var err error
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) > 0 && t[0] == '{' {
		vars, err = jsonSecretVars(label, b)
	} else {
		vars, err = dotenvVars(label, string(b))
	}
	if err != nil {
		return nil, fail("bad_request", err.Error()).withFix(secretBulkFix...)
	}
	if len(vars) == 0 {
		return nil, fail("bad_request", "no secrets in "+label).withFix(secretBulkFix...)
	}
	if len(vars) > mgmt.SecretMaxPerWorkload {
		return nil, fail("bad_request", fmt.Sprintf("%s has %d secrets; a workload has at most %d", label, len(vars), mgmt.SecretMaxPerWorkload))
	}
	first := map[string]int{}
	for _, v := range vars {
		if !mgmt.ValidSecretName(v.name) {
			// Not echoed: a value may have landed where the name goes.
			return nil, fail("bad_request", fmt.Sprintf("%s line %d: the secret name must be a JS identifier and environment variable name ([A-Za-z_][A-Za-z0-9_]*, ≤128), not starting with TW_ or __TW", label, v.line))
		}
		if at, ok := first[v.name]; ok {
			return nil, fail("bad_request", fmt.Sprintf("%s line %d: %s is given twice (first on line %d); give each secret once", label, v.line, v.name, at))
		}
		first[v.name] = v.line
		if len(v.value) > mgmt.SecretMaxValueBytes {
			return nil, fail("bad_request", fmt.Sprintf("%s line %d: the value of %s is over %d bytes", label, v.line, v.name, mgmt.SecretMaxValueBytes))
		}
	}
	return vars, nil
}

// jsonSecretVars parses a JSON object of string values, in order, repeats
// included. Errors name the line, never the decoder's message (it can
// quote the input).
func jsonSecretVars(label string, b []byte) ([]dotenvVar, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	line := func() int { return 1 + bytes.Count(b[:min(int(d.InputOffset()), len(b))], []byte("\n")) }
	bad := func() error {
		return fmt.Errorf("%s line %d: not a JSON object of string values ({\"NAME\": \"value\", …})", label, line())
	}
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, bad()
	}
	var out []dotenvVar
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		if err != nil || !ok {
			return nil, bad()
		}
		at := line()
		if !mgmt.ValidSecretName(k) {
			return nil, fmt.Errorf("%s line %d: the secret name must be a JS identifier and environment variable name ([A-Za-z_][A-Za-z0-9_]*, ≤128), not starting with TW_ or __TW", label, at)
		}
		t, err = d.Token()
		if err != nil {
			return nil, bad()
		}
		v, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("%s line %d: the value of %s must be a JSON string", label, at, k)
		}
		out = append(out, dotenvVar{name: k, value: v, line: at})
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return nil, bad()
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, bad()
	}
	return out, nil
}
