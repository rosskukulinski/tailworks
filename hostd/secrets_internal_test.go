package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

func TestApplySecrets(t *testing.T) {
	t.Parallel()
	app := &bundle.App{Name: "w"}
	if got, err := applySecrets(app, bundle.TierProcess, "w", nil); err != nil || got != app {
		t.Fatalf("no secrets: %v %v", got, err)
	}
	vals := map[string]string{"API_KEY": "v1"}
	got, err := applySecrets(app, bundle.TierProcess, "w", vals)
	if err != nil || got == app || got.Secrets["API_KEY"] != "v1" || app.Secrets != nil {
		t.Fatalf("process tier: %+v %v (the shared app must stay untouched)", got, err)
	}
	// microVM: refused with a typed error naming the secrets, never the values.
	_, err = applySecrets(app, bundle.TierMicroVM, "w", vals)
	if ae := asAPIError(err); ae == nil || ae.Code != "secrets_unsupported_tier" || !strings.Contains(ae.Detail, "API_KEY") || strings.Contains(ae.Detail, "v1") {
		t.Fatalf("microvm: %v", err)
	}
	node := &bundle.App{Runtime: bundle.RuntimeNode, Node: &bundle.NodeApp{Env: map[string]string{"A": "env", "B": "env"}}}
	if _, err := applySecrets(node, bundle.TierProcess, "w", map[string]string{"PORT": "1"}); asAPIError(err).Code != "bad_secret_name" {
		t.Fatalf("node reserved: %v", err)
	}
	if _, err := applySecrets(app, bundle.TierProcess, "w", map[string]string{"TW_X": "1"}); asAPIError(err).Code != "bad_secret_name" {
		t.Fatalf("TW_ prefix: %v", err)
	}
	ns, err := applySecrets(node, bundle.TierProcess, "w", map[string]string{"B": "secret", "C": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	// Secrets never go in the environment, only in the preload's file.
	if env := nodeEnv(ns); env["B"] != "env" || env["C"] != "" {
		t.Fatalf("node env %v carries secrets", env)
	}
	dir := t.TempDir()
	args, err := writeNodeSecrets(dir, ns.Secrets)
	if err != nil || len(args) != 2 || args[0] != "--require" || args[1] != filepath.Join(dir, nodeSecretsPreload) {
		t.Fatalf("args %v, %v", args, err)
	}
	st, err := os.Stat(filepath.Join(dir, nodeSecretsFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file: %v %v", st, err)
	}
	if args, _ := writeNodeSecrets(t.TempDir(), nil); args != nil {
		t.Fatalf("no secrets: args %v", args)
	}
}
