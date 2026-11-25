package hostd

// Worker secrets (ADR-034), host side. When hostd starts a
// deployment it asks mgmt for the workload's secrets at the lease epoch of
// the assignment (mgmt releases them only to the lease holder). They go
// into the instance only:
//
//   - workerd (process tier): text bindings in config.capnp and the facets'
//     env in tw-config.json, both in the 0700 instance directory;
//   - runtime "node": environment variables of the node process.
//
// They are never logged and never written anywhere else. The microVM tier
// is refused for now: its config drive (the instance directory as a tar)
// is written to the VM's run directory on a cold boot, outside the
// instance directory.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// nodeReservedEnv are the node process variables tailworks sets itself; neither
// node.env nor a secret may replace them.
var nodeReservedEnv = map[string]bool{
	"PATH":               true,
	"HOME":               true,
	"TMPDIR":             true,
	"NODE_ENV":           true,
	"NODE_USE_ENV_PROXY": true,
	"HTTP_PROXY":         true,
	"HTTPS_PROXY":        true,
	"http_proxy":         true,
	"https_proxy":        true,
	"NO_PROXY":           true,
	"no_proxy":           true,
	"PORT":               true,
	"HOST":               true,
}

// withSecrets returns app with the workload's secrets (a copy; app is
// shared with the workload's state), or app itself when there are none.
func (h *Host) withSecrets(ctx context.Context, w *workload, app *bundle.App, tier string, epoch int64) (*bundle.App, error) {
	vals, err := h.mgmt.WorkloadSecrets(ctx, w.tenantID, w.name, epoch)
	if err != nil {
		return nil, fmt.Errorf("workload secrets: %w", err)
	}
	out, err := applySecrets(app, tier, w.name, vals)
	if err == nil && len(vals) > 0 {
		h.log.Info("workload secrets released", "workload", w.name, "epoch", epoch, "n", len(vals))
	}
	return out, err
}

// applySecrets checks vals against the tier and runtime and returns app
// with them (a copy), or app itself when there are none.
func applySecrets(app *bundle.App, tier, workload string, vals map[string]string) (*bundle.App, error) {
	if len(vals) == 0 {
		return app, nil
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	if tier == bundle.TierMicroVM {
		return nil, &apiError{Code: "secrets_unsupported_tier", Detail: fmt.Sprintf(
			"workload %q has %d secret(s) (%s), and the microvm tier does not support secrets yet. "+
				"Deploy it with \"tier\": \"process\" in tailworks.jsonc, or remove them (tw secret rm <NAME>); see docs/secrets.md",
			workload, len(names), strings.Join(names, ", "))}
	}
	for _, k := range names {
		if strings.HasPrefix(k, "TW_") || strings.HasPrefix(k, "__TW") {
			return nil, &apiError{Code: "bad_secret_name", Detail: fmt.Sprintf("secret %q uses a name tw reserves (TW_, __TW)", k)}
		}
		if app.Runtime == bundle.RuntimeNode && nodeReservedEnv[k] {
			return nil, &apiError{Code: "bad_secret_name", Detail: fmt.Sprintf(
				"secret %q would replace an environment variable tailworks sets for runtime \"node\"; rename it (tw secret rm %s, then tw secret put <NEW_NAME>)", k, k)}
		}
	}
	cp := *app
	cp.Secrets = vals
	return &cp, nil
}

// nodeEnv is a node app's node.env (and deploy vars). Secrets are NOT
// in it: they reach the app through nodeSecretsPreload, never the process's
// exec-time environment. On macOS any process of the same user can read
// another's initial environment (sysctl KERN_PROCARGS2), and Seatbelt
// can't refuse it; Landlock hides /proc on Linux, but one path for both is
// simpler to reason about.
func nodeEnv(app *bundle.App) map[string]string {
	return app.Node.Env
}

// Node secrets preload: spawnNode writes the values to a 0600 file in the
// instance dir (which only this workload's sandbox may read) and starts
// node with `--require` of a preload that copies them into process.env,
// overriding node.env of the same name, and deletes the file. Every
// (re)start writes it again.
const (
	nodeSecretsFile    = ".tailworks-secrets.json"
	nodeSecretsPreload = ".tailworks-secrets.cjs"
)

const nodeSecretsPreloadJS = `"use strict";
// tailworks: load this workload's secrets into process.env, then delete them.
const fs = require("fs");
const p = require("path").join(__dirname, ` + "`" + nodeSecretsFile + "`" + `);
let s;
try {
  s = JSON.parse(fs.readFileSync(p, "utf8"));
} catch (e) {
  if (e.code !== "ENOENT") throw e; // consumed already (a cluster fork): inherited env
}
if (s) {
  fs.unlinkSync(p);
  Object.assign(process.env, s);
}
`

// writeNodeSecrets writes the secrets file and the preload into dir and
// returns the node arguments that load them (none without secrets).
func writeNodeSecrets(dir string, secrets map[string]string) ([]string, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	sp := filepath.Join(dir, nodeSecretsFile)
	os.Remove(sp)
	if err := os.WriteFile(sp, b, 0o600); err != nil {
		return nil, err
	}
	pp := filepath.Join(dir, nodeSecretsPreload)
	if err := os.WriteFile(pp, []byte(nodeSecretsPreloadJS), 0o600); err != nil {
		os.Remove(sp)
		return nil, err
	}
	return []string{"--require", pp}, nil
}
