package hostd

import (
	"os"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/procsb"
)

// The Landlock sandbox (Linux) has no deny rules: sandboxDeny's paths are
// kept from workloads only because nothing allows them. A state dir, run
// dir, credential file or home dir under one of the trees every profile
// allows reading (/usr, /etc, /opt, …: procsb.ReadableTrees) would be
// readable by every tenant's workerd and Node app, file modes
// notwithstanding (same uid). hostd refuses such a layout at start and when
// it first serves a tenant (issue #342, sandbox_layout_unsafe); the procsb
// launcher refuses the profile too, and tw doctor probes the real paths.
// On macOS, Seatbelt's deny rules win inside an allowed directory, so the
// check has nothing to do (procsb.ReadableTrees is empty).

// checkSandboxLayout refuses cfg's data dir, run dir, extra deny paths and
// the home dir under a sandbox-readable tree when the sandbox is Landlock.
func checkSandboxLayout(sandbox string, cfg Config) error {
	paths := []procsb.LayoutPath{
		{What: "hostd data dir", Path: cfg.DataDir},
		{What: "hostd run dir", Path: cfg.RunDir},
	}
	for _, p := range cfg.SandboxDeny {
		paths = append(paths, procsb.LayoutPath{What: "sandbox-deny path", Path: p})
	}
	if home, err := os.UserHomeDir(); err == nil && home != "/" && home != "" {
		paths = append(paths, procsb.LayoutPath{What: "home dir", Path: home})
	}
	return sandboxLayout(sandbox, paths)
}

// checkTenantLayout refuses a tenant whose credential file is under a
// sandbox-readable tree (the tenant's mint credential, A2).
func checkTenantLayout(sandbox string, t *mgmtapi.Tenant) error {
	return sandboxLayout(sandbox, []procsb.LayoutPath{
		{What: "credential file of tenant " + t.TenantID, Path: t.APIKeyFile},
		{What: "credential file of tenant " + t.TenantID, Path: t.OAuthClientFile},
	})
}

func sandboxLayout(sandbox string, paths []procsb.LayoutPath) error {
	if sandbox != SandboxLandlock {
		return nil
	}
	return procsb.CheckLayout(paths, procsb.ReadableTrees())
}
