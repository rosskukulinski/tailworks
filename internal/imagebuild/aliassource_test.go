package imagebuild

import (
	"context"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/registry"
)

// TestAliasContextFromSource: an alias target that is neither in
// tailworks' registry nor a local Docker image is built from the alias's
// digest-pinned Source (docker-image://); without a Source it is the
// push-it error as before.
func TestAliasContextFromSource(t *testing.T) {
	ctx := context.Background()
	const d = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	a := registry.Alias{From: "docker.io/cloudflare/sandbox:0.12.14", To: "tw.local/cloudflare-sandbox:0.12.14-arm64", Arch: "arm64",
		Source: "ghcr.io/example/cloudflare-sandbox:0.12.14-arm64@" + d}
	o := Options{Tenant: "tna", Aliases: []registry.Alias{a}, Docker: "/nonexistent/docker"}
	got, cleanup, err := aliasContexts(ctx, "FROM cloudflare/sandbox:0.12.14\n", "arm64", t.TempDir(), o)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "cloudflare/sandbox:0.12.14=docker-image://"+a.Source {
		t.Fatalf("contexts %q", got)
	}
	// amd64 builds don't apply the arm64 alias at all.
	if got, _, err := aliasContexts(ctx, "FROM cloudflare/sandbox:0.12.14\n", "amd64", t.TempDir(), o); err != nil || len(got) != 0 {
		t.Fatalf("amd64: %q %v", got, err)
	}
	// A tag-only Source is refused (it could move under the build).
	o.Aliases[0].Source = "ghcr.io/example/cloudflare-sandbox:0.12.14-arm64"
	if _, _, err := aliasContexts(ctx, "FROM cloudflare/sandbox:0.12.14\n", "arm64", t.TempDir(), o); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tag-only source: %v", err)
	}
	// No Source (the placeholder state): today's error.
	o.Aliases[0].Source = ""
	if _, _, err := aliasContexts(ctx, "FROM cloudflare/sandbox:0.12.14\n", "arm64", t.TempDir(), o); err == nil || !strings.Contains(err.Error(), "tw image push") {
		t.Fatalf("no source: %v", err)
	}
}
