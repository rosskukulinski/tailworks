package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/oci"
)

// LocalRegistry is the registry host of references into tailworks' own
// registry: "tw.local/<tenantID>/<name>…" (what tw deploy builds and
// pushes) and "tw.local/<name>…" (the operator's public namespace: the
// alias targets, e.g. tw.local/cloudflare-sandbox:0.12.14-arm64).
const LocalRegistry = "tw.local"

// Alias swaps an upstream reference for another on one architecture
// (ADR-032 D3: Cloudflare publishes cloudflare/sandbox for linux/amd64
// only; arm64 hosts get tailworks' rebuild from the Apache-2.0 source).
type Alias struct {
	From string `json:"from"`
	To   string `json:"to"`
	Arch string `json:"arch"` // "" = every arch

	// Source, if set, is a digest-pinned external image that fills To
	// when To is not in tailworks' registry yet (aliassource.go).
	Source string `json:"source,omitempty"`
}

// DefaultAliases are tailworks' arm64 rebuilds (guest/images/README.md).
// mgmt fills each target from its published Source on first use; without
// one (or with -no-mirror) the operator pushes it (tw image push).
var DefaultAliases = []Alias{
	{From: "docker.io/cloudflare/sandbox:0.12.14", To: "tw.local/cloudflare-sandbox:0.12.14-arm64", Arch: "arm64", Source: sandboxSource("0.12.14")},
	{From: "docker.io/cloudflare/sandbox:1.0.0", To: "tw.local/cloudflare-sandbox:1.0.0-arm64", Arch: "arm64", Source: sandboxSource("1.0.0")},
	{From: "docker.io/cloudflare/sandbox:1.0.1", To: "tw.local/cloudflare-sandbox:1.0.1-arm64", Arch: "arm64", Source: sandboxSource("1.0.1")},
}

// AliasFor returns the alias target of ref on arch ("" = none).
func AliasFor(aliases []Alias, ref, arch string) string {
	a, _ := aliasFor(aliases, ref, arch)
	return a.To
}

// PublicImages finds an operator-marked public base by its upstream
// reference (normalised) and arch.
type PublicImages interface {
	LookupPublic(ctx context.Context, ref, arch string) (repo, digest string, ok bool, err error)
}

// Resolver pins container images at deploy.
type Resolver struct {
	Reg *Registry
	// Upstream returns a client for an upstream registry host (mirroring
	// through mgmt's egress); nil refuses external references.
	Upstream func(host string) *oci.Client
	Aliases  []Alias
	Public   PublicImages
}

// LocalRepo maps a tw.local reference's repository into the registry: a
// tenant's own namespace stays, everything else is public/.
func LocalRepo(tenant, repo string) string {
	if Namespace(repo) == tenant || Namespace(repo) == PublicNamespace {
		return repo
	}
	return PublicNamespace + "/" + repo
}

// MirrorRepo is where an upstream image is mirrored for tenant:
// <tenant>/mirror/<host>/<repository> (":" in a host becomes "-").
func MirrorRepo(tenant string, r oci.Ref) string {
	return tenant + "/mirror/" + strings.NewReplacer(":", "-").Replace(r.Registry) + "/" + r.Repository
}

// PinError is one image that could not be pinned for one arch.
type PinError struct {
	Class, Ref, Arch string
	Err              error
}

func (e *PinError) Error() string {
	return fmt.Sprintf("containers.%s: %s (linux/%s): %v", e.Class, e.Ref, e.Arch, e.Err)
}

// Pin resolves every image of classes for each arch into tenant's view of
// the registry and returns the classes with Pins set, plus the arches for
// which every image resolved. Errors for the other arches are returned
// (all of them, joined) only when no arch resolved.
func (rs *Resolver) Pin(ctx context.Context, tenant string, classes []container.Class, arches []string) ([]container.Class, []string, error) {
	out := make([]container.Class, len(classes))
	failed := map[string][]error{}
	type key struct{ ref, arch string }
	cache := map[key]container.Pin{}
	for i, c := range classes {
		c.Pins = nil
		for _, ni := range c.Named() {
			name, ref := ni[0], ni[1]
			if ref == "" || strings.HasPrefix(ref, "unbuilt:") {
				src := c.Source
				if name != "" {
					src = "images." + name
				}
				return nil, nil, fmt.Errorf("containers.%s: image %s is a Dockerfile tw deploy has not built (run `tw deploy`: it builds the image locally and pushes it to tailworks' registry)", c.Class, src)
			}
			for _, arch := range arches {
				p, ok := cache[key{ref, arch}]
				if !ok {
					var err error
					p, err = rs.pinOne(ctx, tenant, ref, arch)
					if err != nil {
						failed[arch] = append(failed[arch], &PinError{Class: c.Class, Ref: ref, Arch: arch, Err: err})
						continue
					}
					cache[key{ref, arch}] = p
				}
				p.Name = name
				c.Pins = append(c.Pins, p)
			}
		}
		out[i] = c
	}
	var ok []string
	var errs []error
	for _, a := range arches {
		if len(failed[a]) == 0 {
			ok = append(ok, a)
		}
		errs = append(errs, failed[a]...)
	}
	if len(ok) == 0 && len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	for i := range out {
		var keep []container.Pin
		for _, p := range out[i].Pins {
			if len(failed[p.Arch]) == 0 {
				keep = append(keep, p)
			}
		}
		sort.Slice(keep, func(a, b int) bool {
			if keep[a].Name != keep[b].Name {
				return keep[a].Name < keep[b].Name
			}
			return keep[a].Arch < keep[b].Arch
		})
		out[i].Pins = keep
	}
	return out, ok, nil
}

func (rs *Resolver) pinOne(ctx context.Context, tenant, ref, arch string) (container.Pin, error) {
	p := container.Pin{Ref: ref, Arch: arch}
	target := ref
	alias, aliased := aliasFor(rs.Aliases, ref, arch)
	if aliased {
		target = alias.To
		p.From = "alias " + alias.To
	}
	r, err := oci.ParseRef(target)
	if err != nil {
		return p, err
	}
	if r.Registry == LocalRegistry {
		// A tw.local ref into another tenant's namespace can only ever
		// name public/<that path>, never the other tenant's images.
		p.Repository = LocalRepo(tenant, r.Repository)
		d, err := oci.Resolve(ctx, rs.Reg, p.Repository, r.Reference(), arch)
		if err != nil && oci.IsNotFound(err) && aliased && alias.Source != "" {
			d, err = rs.fillAliasTarget(ctx, alias, r, p)
			if err != nil {
				return p, err
			}
		}
		if err != nil {
			if oci.IsNotFound(err) {
				return p, fmt.Errorf("%s is not in tailworks' registry (%s): push it (tw deploy builds and pushes; the operator pushes public images with `tw image push`)", target, p.Repository)
			}
			return p, err
		}
		p.Digest = d
		if p.From == "" {
			p.From = target
		}
		return p, nil
	}
	norm := r.String()
	if rs.Public != nil {
		if repo, d, ok, err := rs.Public.LookupPublic(ctx, norm, arch); err != nil {
			return p, err
		} else if ok {
			p.Repository, p.Digest = repo, d
			p.From = "public " + norm
			return p, nil
		}
	}
	if rs.Upstream == nil {
		return p, fmt.Errorf("%s is an external image and this tw-mgmt does not mirror (no registry egress)", norm)
	}
	repo := MirrorRepo(tenant, r)
	cp, err := oci.Copy(ctx, rs.Upstream(r.Registry), r.Repository, r.Reference(), rs.Reg, repo, []string{arch}, "")
	if err != nil {
		return p, fmt.Errorf("mirror %s: %w", norm, err)
	}
	p.Repository, p.Digest = repo, cp.Platforms[arch]
	p.From = norm + "@" + cp.Digest
	return p, nil
}

// Built are one class's images built and pushed by tw deploy.
type Built struct {
	Image  string            `json:"image,omitempty"`
	Images map[string]string `json:"images,omitempty"`
}

// ApplyBuilt puts the images tw deploy built (a deploy's
// ContainerImages) into the classes. A built ref must be in the tenant's
// own namespace of tailworks' registry (tw.local/<tenantID>/…), so a deploy can
// never point a class at another tenant's image.
func ApplyBuilt(tenant string, ctrs []container.Class, built map[string]Built) ([]container.Class, error) {
	if len(built) == 0 {
		return ctrs, nil
	}
	own := func(class, ref string) error {
		r, err := oci.ParseRef(ref)
		if err != nil {
			return fmt.Errorf("containerImages.%s: %v", class, err)
		}
		if r.Registry != LocalRegistry || Namespace(r.Repository) != tenant {
			return fmt.Errorf("containerImages.%s: %q is not in this tenant's registry namespace (want %s/%s/…)", class, ref, LocalRegistry, tenant)
		}
		return nil
	}
	out := slices.Clone(ctrs)
	for class, b := range built {
		i := slices.IndexFunc(out, func(c container.Class) bool { return c.Class == class })
		if i < 0 {
			return nil, fmt.Errorf("containerImages.%s: the bundle declares no such container class", class)
		}
		c := out[i]
		if b.Image != "" {
			if err := own(class, b.Image); err != nil {
				return nil, err
			}
			c.Image = b.Image
		}
		if len(b.Images) > 0 {
			ims := maps.Clone(c.Images)
			for n, ref := range b.Images {
				if _, ok := ims[n]; !ok {
					return nil, fmt.Errorf("containerImages.%s.images.%s: the class has no image %q", class, n, n)
				}
				if err := own(class, ref); err != nil {
					return nil, err
				}
				ims[n] = ref
			}
			c.Images = ims
		}
		out[i] = c
	}
	return out, nil
}
