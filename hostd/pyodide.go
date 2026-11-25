package hostd

import (
	"context"
	"path/filepath"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/pyodide"
)

// Python Workers (ADR-055, docs/python.md): workerd reads the
// pinned Pyodide bundles from a host directory, read-only, and never
// downloads anything itself (its sandbox has no network).

// pyodideDir is the host's Pyodide bundle directory.
func (h *Host) pyodideDir() string {
	if h.cfg.PyodideDir != "" {
		return h.cfg.PyodideDir
	}
	return filepath.Join(h.cfg.DataDir, "pyodide")
}

// pyodideFor makes sure a Python app's Pyodide bundles are on this host
// (fetching the missing pins once) and returns their directory; "" for
// any other app, and on the microVM tier, whose guest image carries them
// (guestPyodideArgs).
func (h *Host) pyodideFor(app *bundle.App, tier string) (string, error) {
	if !app.Python || tier == bundle.TierMicroVM {
		return "", nil
	}
	dir := h.pyodideDir()
	if len(pyodide.Missing(dir)) > 0 {
		h.log.Info("fetching the pinned Pyodide bundles for Python Workers (once per host)", "dir", dir)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Minute)
	defer cancel()
	if err := pyodide.Ensure(ctx, dir, h.cfg.PyodideURL); err != nil {
		return "", &apiError{Code: "python_unavailable", Detail: err.Error()}
	}
	return dir, nil
}

// guestPyodideArgs are the guest workerd's flags for a Python app on the
// microVM tier: the bundles the guest image carries, read-only
// (guest/build-image.sh).
func guestPyodideArgs(app *bundle.App) []string {
	if !app.Python {
		return nil
	}
	return pyodide.WorkerdArgs(pyodide.GuestDir)
}

// pyodideArgs are workerd's flags for the instance's Pyodide directory.
func (inst *instance) pyodideArgs() []string {
	if inst.pyodide == "" {
		return nil
	}
	return pyodide.WorkerdArgs(inst.pyodide)
}
