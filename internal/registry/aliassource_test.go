package registry

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/oci"
)

// TestPinAliasFillsFromSource: an alias target missing from tailworks'
// registry is filled from the alias's digest-pinned Source into public/
// (tagged as the target, shared by every tenant); without mirroring, or
// without a Source, deploy fails with the push instructions as before.
func TestPinAliasFillsFromSource(t *testing.T) {
	ctx := context.Background()
	g, _ := testRegistry(t)
	up := &oci.MemTarget{}
	upSrv := httptest.NewServer(memRegistry(up))
	t.Cleanup(upSrv.Close)
	idx, per := testIndex(t, up, "0.12.14-arm64", "arm64")
	var hosts []string
	upstream := func(host string) *oci.Client {
		hosts = append(hosts, host)
		return &oci.Client{Base: upSrv.URL, HTTP: upSrv.Client()}
	}
	alias := Alias{From: "docker.io/cloudflare/sandbox:0.12.14", To: "tw.local/cloudflare-sandbox:0.12.14-arm64", Arch: "arm64",
		Source: "ghcr.io/src:0.12.14-arm64@" + idx}
	cls := []container.Class{{Class: "Sandbox", Image: "cloudflare/sandbox:0.12.14"}}

	// -no-mirror: the reworded error names the source and tw image push.
	rs := &Resolver{Reg: g, Aliases: []Alias{alias}}
	_, _, err := rs.Pin(ctx, "tna", cls, []string{"arm64"})
	if err == nil || !strings.Contains(err.Error(), alias.Source) || !strings.Contains(err.Error(), "tw image push") || !strings.Contains(err.Error(), "-no-mirror") {
		t.Fatalf("no-mirror: %v", err)
	}

	rs.Upstream = upstream
	pinned, arches, err := rs.Pin(ctx, "tna", cls, []string{"arm64"})
	if err != nil || len(arches) != 1 {
		t.Fatalf("pin: %v %v", arches, err)
	}
	p := pinned[0].PinFor("cloudflare/sandbox:0.12.14", "arm64")
	if p.Repository != "public/cloudflare-sandbox" || p.Digest != per["arm64"] || p.From != "alias "+alias.To {
		t.Fatalf("pin %+v", p)
	}
	if len(hosts) != 1 || hosts[0] != "ghcr.io" {
		t.Fatalf("upstream hosts %v", hosts)
	}
	// The target is now in public/ under its tag: another tenant pins it
	// without fetching again.
	if d, err := oci.Resolve(ctx, g, "public/cloudflare-sandbox", "0.12.14-arm64", "arm64"); err != nil || d != per["arm64"] {
		t.Fatalf("filled target: %s %v", d, err)
	}
	if p2, _, err := rs.Pin(ctx, "tnb", cls, []string{"arm64"}); err != nil || p2[0].Pins[0].Digest != per["arm64"] || len(hosts) != 1 {
		t.Fatalf("second tenant: %+v %v (fetches %d)", p2, err, len(hosts))
	}
}

// TestPinAliasSourceDigestVerified: a Source whose bytes don't hash to
// its pinned digest fills nothing; a tag-only Source is refused.
func TestPinAliasSourceDigestVerified(t *testing.T) {
	ctx := context.Background()
	g, _ := testRegistry(t)
	up := &oci.MemTarget{}
	upSrv := httptest.NewServer(memRegistry(up))
	t.Cleanup(upSrv.Close)
	testIndex(t, up, "1.0.1-arm64", "arm64")
	other, _ := testIndex(t, &oci.MemTarget{}, "x", "arm64")
	rs := &Resolver{Reg: g, Upstream: func(string) *oci.Client { return &oci.Client{Base: upSrv.URL, HTTP: upSrv.Client()} }}
	cls := []container.Class{{Class: "Sandbox", Image: "cloudflare/sandbox:1.0.1"}}
	for _, src := range []string{"ghcr.io/src:1.0.1-arm64@" + other, "ghcr.io/src:1.0.1-arm64"} {
		rs.Aliases = []Alias{{From: "docker.io/cloudflare/sandbox:1.0.1", To: "tw.local/cloudflare-sandbox:1.0.1-arm64", Arch: "arm64", Source: src}}
		if _, _, err := rs.Pin(ctx, "tna", cls, []string{"arm64"}); err == nil {
			t.Fatalf("source %s: pinned", src)
		}
		if _, err := oci.Resolve(ctx, g, "public/cloudflare-sandbox", "1.0.1-arm64", "arm64"); !oci.IsNotFound(err) {
			t.Fatalf("source %s: target filled (%v)", src, err)
		}
	}
	if err := ValidateAliases([]Alias{{From: "a", To: "b", Source: "ghcr.io/x:1"}}); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tag-only source: %v", err)
	}
	if err := ValidateAliases([]Alias{{From: "a", To: "b", Source: "tw.local/x:1@" + other}}); err == nil {
		t.Fatal("tw.local source accepted")
	}
}

// TestDefaultAliasSources: every published Source in the default map is
// digest-pinned, from SandboxSourceRepo, with the target's tag; a
// placeholder (empty) digest leaves Source unset, so the target must be
// pushed as before.
func TestDefaultAliasSources(t *testing.T) {
	if err := ValidateAliases(DefaultAliases); err != nil {
		t.Fatal(err)
	}
	for _, a := range DefaultAliases {
		v := strings.TrimPrefix(a.From, "docker.io/cloudflare/sandbox:")
		d, ok := sandboxSourceDigests[v]
		if !ok {
			t.Fatalf("%s: no entry in sandboxSourceDigests", a.From)
		}
		if d == "" {
			if a.Source != "" {
				t.Fatalf("%s: placeholder digest but source %q", a.From, a.Source)
			}
			continue
		}
		sr, err := a.SourceRef()
		if err != nil {
			t.Fatal(err)
		}
		to, _ := oci.ParseRef(a.To)
		if sr.Name() != SandboxSourceRepo || sr.Tag != to.Tag || sr.Digest != d {
			t.Fatalf("%s: source %s, want %s:%s@%s", a.From, a.Source, SandboxSourceRepo, to.Tag, d)
		}
	}
}

// TestDefaultAliasSourcesResolve (make test-images, network): each
// published Source's digest resolves on its registry to a linux/arm64
// image. Skipped while every digest is a placeholder.
func TestDefaultAliasSourcesResolve(t *testing.T) {
	if os.Getenv("TW_TEST_IMAGE_SOURCES") != "1" {
		t.Skip("set TW_TEST_IMAGE_SOURCES=1 to resolve the published alias sources (make test-images)")
	}
	n := 0
	for _, a := range DefaultAliases {
		if a.Source == "" {
			continue
		}
		n++
		sr, err := a.SourceRef()
		if err != nil {
			t.Fatal(err)
		}
		c := &oci.Client{Base: oci.RegistryBase(sr.Registry, false), HTTP: oci.DefaultHTTP}
		if _, err := oci.Resolve(context.Background(), c, sr.Repository, sr.Digest, a.Arch); err != nil {
			t.Errorf("%s: %v", a.Source, err)
		}
	}
	if n == 0 {
		t.Skip("every sandbox source digest is a placeholder (internal/registry/aliassource.go): nothing published to check yet")
	}
}
