package main

// Worker secrets (ADR-034), CLI side: `tw secret put|ls|rm`,
// like `wrangler secret`. The value is read from stdin (a hidden prompt
// on a terminal), never from the command line, and is never printed. Both
// on the host and from a `tw link`ed machine the commands go to mgmt's
// /v1 API; mgmt seals the value in custody and releases it only to the
// host holding the workload's lease, on its next deploy.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"golang.org/x/term"
)

// secretInput is where `tw secret put` reads the value (tests replace it):
// r is the input, tty reports a terminal (prompt without echo).
var secretInput = func() (r io.Reader, tty bool, readHidden func() ([]byte, error)) {
	fd := int(os.Stdin.Fd())
	return os.Stdin, term.IsTerminal(fd), func() ([]byte, error) { return term.ReadPassword(fd) }
}

type secretResult struct {
	Workload string               `json:"workload"`
	TenantID string               `json:"tenantID"`
	Secret   *mgmtapi.SecretMeta  `json:"secret,omitempty"`
	Secrets  []mgmtapi.SecretMeta `json:"secrets,omitempty"`
	Deleted  string               `json:"deleted,omitempty"`
	Created  bool                 `json:"created,omitempty"`
}

// secretFlags registers tw secret's flags, the same for every verb.
func secretFlags(f *flags) (tenant, workload *string) {
	return tenantFlag(f), f.String("workload", "", "workload (default: the app in the current directory, as tw deploy names it)")
}

func cmdSecret(args []string) error {
	sub, args, _ := scanVerb(args, func(f *flags) { secretFlags(f) }, func(string) int { return 1 })
	return cmdSecretSub(sub, args)
}

func cmdSecretSub(sub string, args []string) error {
	f := newFlags("secret")
	tenantF, workloadF := secretFlags(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if sub == "" {
		return needsSubcommand("secret")
	}
	if sub == "bulk" {
		return cmdSecretBulk(f, *tenantF, *workloadF, pos)
	}
	switch {
	case sub == "ls" && len(pos) != 0:
		return fail("bad_request", "tw secret ls takes no arguments", "usage: tw "+usageOf("secret"))
	case sub == "put" && len(pos) > 1:
		// Never echo the extra arguments: one of them is probably the value.
		return fail("bad_request", "tw secret put takes the secret's NAME only; the value is read from stdin, never from the command line",
			"usage: tw "+usageOf("secret")).withFix("tw secret put <NAME>            (type the value at the prompt)",
			"printf %s \"$VALUE\" | tw secret put <NAME>")
	case (sub == "put" || sub == "rm") && len(pos) != 1:
		return fail("bad_request", "tw secret "+sub+" needs the secret's NAME", "usage: tw "+usageOf("secret"))
	case sub != "put" && sub != "ls" && sub != "rm":
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q (put, bulk, ls or rm)", sub), "usage: tw "+usageOf("secret"))
	}
	name := ""
	if len(pos) == 1 {
		name = pos[0]
		if !mgmt.ValidSecretName(name) {
			// Not echoed: a mistyped value may have landed here.
			return fail("bad_request", "the secret name must be a JS identifier and environment variable name ([A-Za-z_][A-Za-z0-9_]*, ≤128), not starting with TW_ or __TW")
		}
	}
	workload, err := secretWorkload(*workloadF, sub)
	if err != nil {
		return err
	}
	var value string
	if sub == "put" {
		if value, err = readSecretValue(name); err != nil {
			return err
		}
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/secrets"
	res := secretResult{Workload: workload, TenantID: tn.TenantID}
	hdr := map[string]string{"Tw-Actor": actor()}
	switch sub {
	case "put":
		var meta mgmtapi.SecretMeta
		if err := s.mgmt().DoHeaders(ctx, "PUT", base+"/"+name, map[string]string{"value": value}, &meta, hdr); err != nil {
			return err
		}
		res.Secret, res.Created = &meta, meta.CreatedAt == meta.UpdatedAt
		emit(f, res, func() {
			verb := "updated"
			if res.Created {
				verb = "created"
			}
			fmt.Printf("✓ secret %s %s for workload %s (tenant %s)\n", name, verb, workload, tn.Tailnet)
			fmt.Printf("  env.%s in the workload from its next deploy: tw deploy\n", name)
		})
	case "ls":
		items, err := listAll[mgmtapi.SecretMeta](ctx, s.mgmt(), base, 0)
		if err != nil {
			return err
		}
		res.Secrets = items
		if res.Secrets == nil {
			res.Secrets = []mgmtapi.SecretMeta{}
		}
		emit(f, res, func() {
			if len(items) == 0 {
				fmt.Printf("no secrets for workload %s (tenant %s) · add one: tw secret put <NAME>\n", workload, tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %-20s %s\n", "NAME", "UPDATED", "BY")
			for _, m := range items {
				fmt.Printf("%-32s %-20s %s\n", tsafe(m.Name), time.UnixMilli(m.UpdatedAt).Local().Format("2006-01-02 15:04:05"), tsafe(orDash(m.UpdatedBy)))
			}
		})
	case "rm":
		if err := s.mgmt().DoHeaders(ctx, "DELETE", base+"/"+name, nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = name
		emit(f, res, func() {
			fmt.Printf("✓ secret %s deleted from workload %s (tenant %s)\n", name, workload, tn.Tailnet)
			fmt.Printf("  the workload keeps it until its next deploy: tw deploy\n")
		})
	}
	return nil
}

// secretWorkload is --workload, or else the app in the current directory's
// workload, for tw secret sub.
func secretWorkload(workload, sub string) (string, error) {
	if workload == "" {
		var err error
		if workload, err = appWorkload("secret " + sub); err != nil { // appworkload.go
			return "", err
		}
	}
	if workloadLabel(workload) != workload {
		return "", fail("bad_request", fmt.Sprintf("workload %q is not a DNS label", workload))
	}
	return workload, nil
}

// readSecretValue reads one secret value: a hidden prompt on a terminal,
// else all of stdin less one trailing newline (`echo $V | tw secret put`).
func readSecretValue(name string) (string, error) {
	r, tty, hidden := secretInput()
	var b []byte
	var err error
	if tty {
		fmt.Fprintf(os.Stderr, "Enter a secret value for %s: ", name)
		b, err = hidden()
		fmt.Fprintln(os.Stderr)
	} else {
		b, err = io.ReadAll(io.LimitReader(r, mgmt.SecretMaxValueBytes+2))
	}
	if err != nil {
		return "", fail("bad_request", "can't read the secret value: "+err.Error())
	}
	if !tty {
		b = bytes.TrimSuffix(b, []byte("\n"))
		b = bytes.TrimSuffix(b, []byte("\r"))
	}
	if len(b) > mgmt.SecretMaxValueBytes {
		return "", fail("bad_request", fmt.Sprintf("the secret value is over %d bytes", mgmt.SecretMaxValueBytes))
	}
	return string(b), nil
}
