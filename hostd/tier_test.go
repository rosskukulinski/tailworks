package hostd

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/image"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/hostd/sandbox/sandboxtest"
)

type fakeBackend struct{}

func (fakeBackend) Boot(context.Context, sandbox.Spec) (sandbox.VM, error) { return nil, nil }
func (fakeBackend) Name() string                                           { return "fake" }

// TestTierFor: deployment tier > tenant tier > process. A microvm
// deployment on a host without a microVM backend is refused with
// tier_unavailable; it is never downgraded to the process tier (ADR-032
// D5b). Only ForceProcessTier (tw dev) runs it in the process tier.
func TestTierFor(t *testing.T) {
	t.Parallel()
	ten := func(tier string) *tenant { return &tenant{info: mgmtapi.Tenant{Tier: tier}} }
	const refused = "refused"
	cases := []struct {
		vm, force   bool
		tenant, app string
		want        string
	}{
		{false, false, "", "", "process"},
		{false, false, "", "microvm", refused}, // no backend: refused, not downgraded
		{false, false, "microvm", "", refused}, // the tenant's default too
		{false, false, "microvm", "process", "process"},
		{false, true, "", "microvm", "process"}, // tw dev: explicit
		{false, true, "microvm", "microvm", "process"},
		{true, false, "", "", "process"},
		{true, false, "", "microvm", "microvm"},
		{true, false, "microvm", "", "microvm"},
		{true, false, "microvm", "process", "process"}, // deployment overrides tenant
		{true, true, "", "microvm", "process"},
	}
	for _, c := range cases {
		h := &Host{log: slog.New(slog.DiscardHandler), cfg: Config{ForceProcessTier: c.force}}
		if c.vm {
			h.cfg.VM = fakeBackend{}
		}
		got, err := h.tierFor(&bundle.App{Name: "x", Tier: c.app}, ten(c.tenant))
		if c.want == refused {
			var ae *apiError
			if !errors.As(err, &ae) || ae.Code != "tier_unavailable" || got != "" {
				t.Errorf("%+v: got %q, %v; want a tier_unavailable refusal", c, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q, %v; want %s", c, got, err, c.want)
		}
	}
	// Node apps always run in the process tier (the guest has no Node.js).
	h := &Host{log: slog.New(slog.DiscardHandler)}
	if got, err := h.tierFor(&bundle.App{Name: "n", Runtime: bundle.RuntimeNode, Tier: "microvm"}, nil); err != nil || got != "process" {
		t.Errorf("node app: %q %v", got, err)
	}
}

// The registered and reported tiers follow the backends: "containers" only
// with a usable container backend and image store (they run in microVMs);
// ForceProcessTier advertises neither.
func TestHostTiers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		vm, ctr, force bool
		want           string
	}{{false, false, false, "process"}, {true, false, false, "process,microvm"}, {true, true, false, "process,microvm,containers"},
		{true, true, true, "process"}} {
		h := &Host{cfg: Config{ForceProcessTier: c.force}}
		if c.vm {
			h.cfg.VM = fakeBackend{}
		}
		if c.ctr {
			h.cfg.Containers, h.cfg.Images = &sandboxtest.Procs{}, &image.Store{}
		}
		if got := strings.Join(h.tiers(), ","); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}
