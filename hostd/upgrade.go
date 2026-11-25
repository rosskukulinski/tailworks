package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Self-upgrade (ADR-041 §8): an upgrade run in mgmt
// pushes POST /v1/push/upgrade; a host started with Config.SelfUpgrade
// downloads the release tarball from mgmt (GET /v1/releases/{v}/artifacts/
// {name}, host role), checks its SHA-256 and its release signature against
// the keys pinned in this binary (upgradesig.go), runs the tarball's install.sh
// into Config.InstallPrefix (which keeps the previous install as .prev),
// and calls Config.Restart. tw-hostd then exits for its service manager
// to start the new binary; the run waits for the host to re-register at
// the new version.

// UpgradeOrder is the push body.
type UpgradeOrder struct {
	RunID   string `json:"runID"`
	Version string `json:"version"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

var (
	artifactNameRE = regexp.MustCompile(`^tailworks-[0-9]+\.[0-9]+\.[0-9]+-(darwin|linux)-(amd64|arm64)\.tar\.gz$`)
	sha256RE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type upgrader struct {
	mu      sync.Mutex
	running string // the version being installed ("" = idle)

	// signers overrides the release keys pinned in this binary (tests
	// only; never set from mgmt). upgradesig.go.
	signers []ed25519.PublicKey
}

// upgradeHandler is POST /v1/push/upgrade.
func (h *Host) upgradeHandler(w http.ResponseWriter, r *http.Request) {
	var o UpgradeOrder
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&o); err != nil {
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "invalid JSON"})
		return
	}
	switch {
	case !h.selfUpgradeEnabled():
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "this host does not upgrade itself (start tw-hostd with -self-upgrade under a service manager)"})
		return
	case !artifactNameRE.MatchString(o.Name) || !sha256RE.MatchString(o.SHA256) ||
		!strings.Contains(o.Name, "-"+o.Version+"-"):
		writeAPIError(w, &apiError{Code: "bad_request", Detail: "name, version and sha256 are required and must agree"})
		return
	}
	h.upg.mu.Lock()
	busy := h.upg.running
	if busy == "" {
		h.upg.running = o.Version
	}
	h.upg.mu.Unlock()
	if busy != "" {
		writeJSON(w, 202, map[string]any{"ok": true, "installing": busy})
		return
	}
	go func() {
		err := h.selfUpgrade(context.Background(), o)
		if err != nil {
			h.log.Warn("self-upgrade failed", "version", o.Version, "run", o.RunID, "err", err)
			h.upg.mu.Lock()
			h.upg.running = ""
			h.upg.mu.Unlock()
			return
		}
		h.log.Info("self-upgrade installed; restarting", "version", o.Version, "run", o.RunID)
		h.cfg.Restart()
	}()
	writeJSON(w, 202, map[string]any{"ok": true, "installing": o.Version})
}

func (h *Host) selfUpgradeEnabled() bool {
	return h.cfg.SelfUpgrade && h.cfg.Restart != nil && h.cfg.InstallPrefix != ""
}

// selfUpgrade downloads, verifies and installs o (not the restart).
func (h *Host) selfUpgrade(ctx context.Context, o UpgradeOrder) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	work := filepath.Join(h.cfg.DataDir, "upgrade")
	os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(work)
	tgz := filepath.Join(work, o.Name)
	if err := h.downloadArtifact(ctx, o, tgz); err != nil {
		return err
	}
	return h.installArtifact(ctx, tgz, filepath.Join(work, "x"), o.Version)
}

// installArtifact extracts a release tarball into dir, verifies it (signed
// by a pinned key, and the version that was ordered), and runs its
// install.sh into Config.InstallPrefix.
func (h *Host) installArtifact(ctx context.Context, tgz, dir, version string) error {
	name := filepath.Base(tgz)
	if err := extractTarGz(tgz, dir); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	inst, err := filepath.Glob(filepath.Join(dir, "tailworks-*", "install.sh"))
	if err != nil || len(inst) != 1 {
		return fmt.Errorf("%s has no install.sh", name)
	}
	if err := h.verifyRelease(filepath.Dir(inst[0])); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	// The signature says the release is genuine, not which one: a
	// compromised mgmt could send an older signed release (with a known
	// bug) under the new version's name. VERSION is in the signed list.
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(inst[0]), "VERSION")); err != nil || strings.TrimSpace(string(b)) != version {
		return fmt.Errorf("%s: refusing an unverified release: its VERSION is %q, the order was for %s", name, strings.TrimSpace(string(b)), version)
	}
	cmd := exec.CommandContext(ctx, "sh", inst[0], "--prefix", h.cfg.InstallPrefix, "--force")
	cmd.Env = append(os.Environ(), "TW_HOME="+h.cfg.InstallPrefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install.sh: %v: %s", err, lastLines(string(out), 5))
	}
	return nil
}

func (h *Host) downloadArtifact(ctx context.Context, o UpgradeOrder, dst string) error {
	resp, err := h.mgmt.V1().GetReleaseArtifact(ctx, o.Version, o.Name)
	if err != nil {
		return fmt.Errorf("download %s: %w", o.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download %s: %s: %s", o.Name, resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, 2<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", o.Name, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != o.SHA256 {
		return fmt.Errorf("%s: SHA-256 %s, want %s", o.Name, got, o.SHA256)
	}
	return nil
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " | ")
}
