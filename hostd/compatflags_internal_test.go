package hostd

import (
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/features"
)

// TestFeaturesExperimentalFlags (#337): a host advertises experimental-flags
// only with Config.AllowExperimentalFlags (-allow-experimental-flags), so
// mgmt places deployments that list experimental flags there and nowhere
// else.
func TestFeaturesExperimentalFlags(t *testing.T) {
	on := &Host{cfg: Config{AllowExperimentalFlags: true}}
	if !slices.Contains(on.features(), features.ExperimentalFlags) || slices.Contains((&Host{}).features(), features.ExperimentalFlags) {
		t.Fatalf("features: %v / %v", on.features(), (&Host{}).features())
	}
}
