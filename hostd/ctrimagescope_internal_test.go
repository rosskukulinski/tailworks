package hostd

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/sandbox/sandboxtest"
	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/oci"
	"github.com/rosskukulinski/tailworks/internal/registry"
)

// ctrRegistry is tailworks' registry as a host sees it: admins push, hosts
// pull; the host's manifest pulls are counted.
type ctrRegistry struct {
	srv   *httptest.Server
	adm   *oci.Client
	pulls atomic.Int64
}

func newCtrRegistry(t *testing.T) *ctrRegistry {
	t.Helper()
	r := &ctrRegistry{}
	reg := &registry.Registry{Dir: t.TempDir(), Meta: &registry.MemMeta{}, Authorize: func(req *http.Request, ns string, push bool) error {
		if req.Header.Get("X-Who") == "admin" {
			return nil
		}
		if push {
			return &registry.Denied{Reason: "hosts pull"}
		}
		if req.Method == "GET" && strings.Contains(req.URL.Path, "/manifests/") {
			r.pulls.Add(1)
		}
		return nil
	}}
	r.srv = httptest.NewServer(reg)
	t.Cleanup(r.srv.Close)
	r.adm = &oci.Client{Base: r.srv.URL, HTTP: r.srv.Client(), Edit: func(req *http.Request) { req.Header.Set("X-Who", "admin") }}
	return r
}

// push writes a linux/<host arch> image (entrypoint sleep) whose one file
// holds body into repo, and returns its manifest digest.
func (r *ctrRegistry) push(t *testing.T, repo, body string) string {
	t.Helper()
	ctx := context.Background()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	tw.WriteHeader(&tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "etc/id", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	tw.Write([]byte(body))
	tw.Close()
	cfg, _ := json.Marshal(map[string]any{"architecture": runtime.GOARCH, "os": "linux",
		"config": map[string]any{"Entrypoint": []string{"sleep", "3600"}, "Env": []string{"PATH=/usr/bin:/bin"}, "WorkingDir": "/"}})
	for _, b := range [][]byte{layer.Bytes(), cfg} {
		if _, err := r.adm.PutBlobBytes(ctx, repo, b); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := json.Marshal(oci.Manifest{SchemaVersion: 2, MediaType: oci.MediaTypeManifest,
		Config: &oci.Descriptor{MediaType: oci.MediaTypeConfig, Digest: oci.Digest(cfg), Size: int64(len(cfg))},
		Layers: []oci.Descriptor{{MediaType: oci.MediaTypeLayer, Digest: oci.Digest(layer.Bytes()), Size: int64(layer.Len())}}})
	d, err := r.adm.PutManifest(ctx, repo, oci.Digest(m), oci.MediaTypeManifest, m)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ctrInstPinned is a workload instance whose Box class runs ref, pinned at
// deploy to repo@digest (what mgmt's assignment carries).
func ctrInstPinned(h *Host, tenant, wl, ref, repo, digest string) *instance {
	w := &workload{h: h, tenantID: tenant, name: wl}
	h.mu.Lock()
	h.workloads[tenant+"/"+wl] = w
	h.mu.Unlock()
	app := &bundle.App{Classes: map[string]*bundle.Class{"Box": {Name: "Box", Node: true}},
		Containers: map[string]container.Class{"Box": {Class: "Box", Image: ref, InstanceType: "lite",
			Pins: []container.Pin{{Ref: ref, Arch: runtime.GOARCH, Repository: repo, Digest: digest}}}}}
	return &instance{h: h, w: w, app: app}
}

// TestContainerStartRefusesAnotherTenantsPin (security ledger C-imagecache,
// ADR-032 D3): the container start path (ctrManager.start, what the
// /v1/container handler runs) accepts only a pin in the tenant's own
// registry namespace or in public/. With tenant A's image already pulled
// and converted into the host's cache, an assignment of tenant B that names
// A's repository and digest is refused before anything is fetched or
// booted; B's own push of the same bytes (same digest) starts from its own
// pull and conversion in B's cache scope, never from A's entry.
func TestContainerStartRefusesAnotherTenantsPin(t *testing.T) {
	t.Parallel()
	reg := newCtrRegistry(t)
	st, err := sandboxtest.ImageStore(t.TempDir(), []string{"sleep", "3600"})
	if err != nil {
		t.Fatal(err)
	}
	st.Sources = nil // pinned images only: nothing on the host's disk
	st.Puller = image.RegistryPuller{Client: &oci.Client{Base: reg.srv.URL, HTTP: reg.srv.Client()}}
	h, procs := ctrHost(t, func(c *Config) { c.Images = st })

	const ref = "tw.local/app:1"
	dA := reg.push(t, "tna/app", "same bytes")
	dB := reg.push(t, "tnb/app", "same bytes")
	if dA != dB {
		t.Fatal("expected identical digests")
	}

	// Tenant A starts its container: one pull, into A's cache scope.
	if err := ctrStart(t, h, ctrInstPinned(h, "tna", "w", ref, "tna/app", dA), "a", startReq{}); err != nil {
		t.Fatal(err)
	}
	if procs.Boots.Load() != 1 || reg.pulls.Load() != 1 {
		t.Fatalf("A's start: %d boots, %d pulls; want 1 and 1", procs.Boots.Load(), reg.pulls.Load())
	}
	cacheA := filepath.Join(st.Dir, "tna", strings.TrimPrefix(dA, "sha256:")+".erofs")
	if _, err := os.Stat(cacheA); err != nil {
		t.Fatalf("A's image is not in the host's cache: %v", err)
	}

	// Tenant B's assignment names A's repository and digest: refused, no
	// registry traffic, no boot, nothing cached for B.
	err = ctrStart(t, h, ctrInstPinned(h, "tnb", "w", ref, "tna/app", dA), "b", startReq{})
	if codeOf(err) != "image_unavailable" || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), `namespace "tna"`) {
		t.Fatalf("B naming A's pin: want image_unavailable (refused), got %v", err)
	}
	if procs.Boots.Load() != 1 || reg.pulls.Load() != 1 {
		t.Fatalf("B's refused start: %d boots, %d pulls; want still 1 and 1", procs.Boots.Load(), reg.pulls.Load())
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "tnb")); !os.IsNotExist(err) {
		t.Fatalf("a refused start left a cache scope for B: %v", err)
	}

	// B's own image, byte-identical: a second pull and conversion into
	// B's scope, not A's cached entry.
	if err := ctrStart(t, h, ctrInstPinned(h, "tnb", "w2", ref, "tnb/app", dB), "b", startReq{}); err != nil {
		t.Fatal(err)
	}
	if procs.Boots.Load() != 2 || reg.pulls.Load() != 2 {
		t.Fatalf("B's own start: %d boots, %d pulls; want 2 and 2", procs.Boots.Load(), reg.pulls.Load())
	}
	cacheB := filepath.Join(st.Dir, "tnb", strings.TrimPrefix(dB, "sha256:")+".erofs")
	if _, err := os.Stat(cacheB); err != nil {
		t.Fatalf("B's image is not in B's cache scope: %v", err)
	}
	if specs := procs.Specs; len(specs) != 2 || specs[0].Image != cacheA || specs[1].Image != cacheB {
		t.Fatalf("booted images %+v; want A's %s then B's %s", specs, cacheA, cacheB)
	}
}
