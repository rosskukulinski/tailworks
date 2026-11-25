package hostd

import (
	"github.com/rosskukulinski/tailworks/internal/features"
	"github.com/rosskukulinski/tailworks/internal/version"
)

// FeatureSelfUpgrade: this hostd installs a release pushed by an upgrade
// run and restarts itself (Config.SelfUpgrade; upgrade.go).
const FeatureSelfUpgrade = "self-upgrade"

// version is what this host reports (ADR-041 §7).
func (h *Host) version() string {
	if h.cfg.Version != "" {
		return h.cfg.Version
	}
	return version.Version
}

// features is what this host offers placement (internal/features), plus
// self-upgrade when it can install a pushed release.
func (h *Host) features() []string {
	fs := features.Host()
	if h.cfg.SelfUpgrade && h.cfg.Restart != nil && h.cfg.InstallPrefix != "" {
		fs = append(fs, FeatureSelfUpgrade)
	}
	if h.cfg.WorkerInternet.ExitNode() {
		fs = append(fs, features.InternetExitNode)
	}
	if h.cfg.AllowExperimentalFlags {
		fs = append(fs, features.ExperimentalFlags)
	}
	return fs
}
