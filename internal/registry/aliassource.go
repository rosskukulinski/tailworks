package registry

import (
	"context"
	"fmt"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/oci"
)

// SandboxSourceRepo is where tailworks publishes its arm64 rebuilds of
// cloudflare/sandbox (.github/workflows/publish-sandbox-images.yml, from
// guest/images/build-sandbox-images.sh). Tags are immutable
// (<version>-arm64); aliases name them by digest.
const SandboxSourceRepo = "ghcr.io/rosskukulinski/cloudflare-sandbox"

// sandboxSourceDigests are the published digests of the arm64 rebuilds, by
// sandbox version.
//
// PLACEHOLDER: empty until the maintainer runs the publish workflow. An
// empty digest means "no Source": the alias target must be pushed by the
// operator (`tw image push`), as before. Bump these in a normal PR with
// the digests the workflow prints; `make test-images` then checks each
// one resolves.
var sandboxSourceDigests = map[string]string{
	"0.12.14": "", // PLACEHOLDER: sha256:… of ghcr.io/rosskukulinski/cloudflare-sandbox:0.12.14-arm64
	"1.0.0":   "", // PLACEHOLDER: sha256:… of ghcr.io/rosskukulinski/cloudflare-sandbox:1.0.0-arm64
	"1.0.1":   "", // PLACEHOLDER: sha256:… of ghcr.io/rosskukulinski/cloudflare-sandbox:1.0.1-arm64
}

// sandboxSource is the digest-pinned published rebuild of sandbox version
// v ("" while its digest is a placeholder).
func sandboxSource(v string) string {
	d := sandboxSourceDigests[v]
	if d == "" {
		return ""
	}
	return SandboxSourceRepo + ":" + v + "-arm64@" + d
}

// SourceRef parses a's Source, which must be pinned by digest (a tag
// alone could be moved under every tenant that pins the alias).
func (a Alias) SourceRef() (oci.Ref, error) {
	r, err := oci.ParseRef(a.Source)
	if err != nil {
		return r, err
	}
	if r.Digest == "" {
		return r, fmt.Errorf("alias %s: source %s is not pinned by digest (want …@sha256:…)", a.From, a.Source)
	}
	if r.Registry == LocalRegistry {
		return r, fmt.Errorf("alias %s: source %s must be an external registry, not %s", a.From, a.Source, LocalRegistry)
	}
	return r, nil
}

// ValidateAliases checks an alias map (e.g. tw-mgmt -image-aliases).
func ValidateAliases(as []Alias) error {
	for _, a := range as {
		if a.From == "" || a.To == "" {
			return fmt.Errorf("alias %+v: from and to are required", a)
		}
		if a.Source != "" {
			if _, err := a.SourceRef(); err != nil {
				return err
			}
		}
	}
	return nil
}

// aliasFor is the alias that applies to ref on arch.
func aliasFor(aliases []Alias, ref, arch string) (Alias, bool) {
	n := oci.Normalize(ref)
	for _, a := range aliases {
		if oci.Normalize(a.From) == n && (a.Arch == "" || a.Arch == arch) {
			return a, true
		}
	}
	return Alias{}, false
}

// fillAliasTarget copies a's Source into the public namespace as its
// target (repo:tag), digest-verified, so every tenant's deploy pins the
// same bytes the operator would otherwise push with `tw image push`.
func (rs *Resolver) fillAliasTarget(ctx context.Context, a Alias, target oci.Ref, p container.Pin) (string, error) {
	sr, err := a.SourceRef()
	if err != nil {
		return "", err
	}
	if rs.Upstream == nil {
		return "", fmt.Errorf("%s is not in tailworks' registry (%s), and this tw-mgmt does not mirror (-no-mirror), so it can't fetch it from %s: push it with `tw image push` (see docs/containers.md \"Public bases\")", target, p.Repository, a.Source)
	}
	tag := ""
	if target.Digest == "" {
		tag = target.Tag
	}
	cp, err := oci.Copy(ctx, rs.Upstream(sr.Registry), sr.Repository, sr.Digest, rs.Reg, p.Repository, []string{p.Arch}, tag)
	if err != nil {
		return "", fmt.Errorf("%s is not in tailworks' registry (%s) and fetching it from %s failed: %w (or push it with `tw image push`)", target, p.Repository, a.Source, err)
	}
	if target.Digest != "" && target.Digest != cp.Digest && target.Digest != cp.Platforms[p.Arch] {
		return "", fmt.Errorf("alias %s: source %s does not match the target's digest %s", a.From, a.Source, target.Digest)
	}
	return cp.Platforms[p.Arch], nil
}
