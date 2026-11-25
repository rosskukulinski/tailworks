package gen

import (
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/compatflags"
)

// Experimental compatibility flags (#337, ledger H-compat-flags). Every
// workerd here runs with --experimental for the platform worker, so a
// tenant's compatibility_flags would unlock every experimental flag
// (unsafe_module: workerd:unsafe and eval; rtti_api; precise_timers; and
// whatever a workerd bump adds). Generate refuses them unless the operator
// allows them (Options.AllowExperimentalFlags, tw-hostd
// -allow-experimental-flags). This is the one place every path goes
// through: the tenant service's flags and the facets' Dynamic Workers both
// come from tenantFlags(app.CompatFlags).

// ErrExperimentalFlag is the typed code of ExperimentalFlagError
// (docs/errors.md#experimental_flag).
const ErrExperimentalFlag = "experimental_flag"

// ExperimentalFlagError is Generate's refusal of experimental flags.
type ExperimentalFlagError struct {
	Flags []string // the experimental ones, sorted
}

func (e *ExperimentalFlagError) Error() string {
	return ErrExperimentalFlag + ": compatibility flags " + strings.Join(e.Flags, ", ") +
		" are experimental in workerd (it refuses them without --experimental) and this host does not allow them for deployments; " +
		"remove them from compatibility_flags, or have the operator start tw-hostd with -allow-experimental-flags (docs/COMPAT.md#experimental-flags)"
}

// checkCompatFlags refuses experimental flags under the default policy:
// those the known list names (internal/compatflags), plus whatever the
// workerd at o.WorkerdBin itself refuses, when set. A probe that can't run
// is not an error here: the list still applies, and the real start reports
// what it refuses.
func checkCompatFlags(flags []string, o Options) error {
	if o.AllowExperimentalFlags || len(flags) == 0 {
		return nil
	}
	exp := compatflags.Filter(flags)
	if o.WorkerdBin != "" {
		if more, err := compatflags.Probe(o.WorkerdBin, flags); err == nil {
			exp = append(exp, more...)
		}
	}
	if len(exp) == 0 {
		return nil
	}
	slices.Sort(exp)
	return &ExperimentalFlagError{Flags: slices.Compact(exp)}
}
