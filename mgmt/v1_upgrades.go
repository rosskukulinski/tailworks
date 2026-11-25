package mgmt

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/internal/catalog"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// /v1 handlers for cordon, drain, releases and upgrade runs
// (ADR-041; hostops.go, upgrades.go).

func (a *v1api) CordonHost(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	h, err := a.s.M.CordonHost(r.Context(), id, true, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, a.hostView(r, h))
}

func (a *v1api) UncordonHost(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	h, err := a.s.M.CordonHost(r.Context(), id, false, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, a.hostView(r, h))
}

func (a *v1api) DrainHost(w http.ResponseWriter, r *http.Request, id v1.HostID) {
	var o DrainOptions
	if r.ContentLength != 0 {
		if err := readJSON(r, &o); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	res, err := a.s.M.DrainHost(r.Context(), id, o, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (a *v1api) ListReleases(w http.ResponseWriter, r *http.Request) {
	es, err := a.s.M.Catalog(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"current": a.s.M.Version(), "items": es})
}

func (a *v1api) ImportReleases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []catalog.Entry `json:"entries"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Entries) == 0 {
		writeErr(w, r, errf(400, "bad_request", "entries is empty"))
		return
	}
	if err := a.s.M.ImportReleases(r.Context(), req.Entries, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	a.ListReleases(w, r)
}

// maxArtifactBytes bounds an uploaded release tarball.
const maxArtifactBytes = 2 << 30

func (a *v1api) PutReleaseArtifact(w http.ResponseWriter, r *http.Request, ver string, name string) {
	m := a.s.M
	dir := m.releaseDir(ver)
	if dir == "" {
		writeErr(w, r, errf(409, "bad_request", "this tw-mgmt keeps no release artifacts (no release directory)"))
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, r, err)
		return
	}
	want := ""
	if name != "SHA256SUMS" {
		if want = m.artifactSum(ver, name); want == "" {
			writeErr(w, r, errf(400, "bad_request", "upload SHA256SUMS for %s first (it must list %s)", ver, name))
			return
		}
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), http.MaxBytesReader(w, r.Body, maxArtifactBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		writeErr(w, r, errf(400, "bad_request", "upload: %v", err))
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if want != "" && sum != want {
		writeErr(w, r, errf(400, "bad_request", "%s: SHA-256 %s does not match SHA256SUMS (%s)", name, sum, want))
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		writeErr(w, r, err)
		return
	}
	m.audit(r.Context(), AuditRecord{Kind: "release_artifact", ActingFor: principalOf(r.Context()).Actor(r)},
		map[string]any{"version": ver, "name": name, "sha256": sum, "size": n})
	writeJSON(w, 201, map[string]any{"version": ver, "name": name, "sha256": sum, "size": n})
}

func (a *v1api) GetReleaseArtifact(w http.ResponseWriter, r *http.Request, ver string, name string) {
	dir := a.s.M.releaseDir(ver)
	if dir == "" {
		writeErr(w, r, errf(404, "not_found", "this tw-mgmt keeps no release artifacts"))
		return
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "release %s has no %s on this tw-mgmt replica (tw upgrade import it)", ver, name))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (a *v1api) ListUpgrades(w http.ResponseWriter, r *http.Request) {
	runs, err := a.s.M.ListUpgrades(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": runs})
}

func (a *v1api) CreateUpgrade(w http.ResponseWriter, r *http.Request) {
	var req UpgradeRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.DryRun {
		p, err := a.s.M.PlanUpgrade(r.Context(), req)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, p)
		return
	}
	run, err := a.s.M.StartUpgrade(r.Context(), req, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	location(w, "/v1/upgrades/"+run.ID)
	writeJSON(w, 201, run)
}

func (a *v1api) GetUpgrade(w http.ResponseWriter, r *http.Request, id string) {
	run, _, err := a.s.M.GetUpgrade(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, run)
}

func (a *v1api) ActOnUpgrade(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	run, err := a.s.M.ActOnUpgrade(r.Context(), id, req.Action, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, run)
}
