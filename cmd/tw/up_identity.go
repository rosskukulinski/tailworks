package main

import (
	"github.com/rosskukulinski/tailworks/internal/hostdrun"
	"github.com/rosskukulinski/tailworks/internal/mgmtd"
)

// identitySingle applies the saved fast-identity settings (--warm-keys,
// --pool-nodes, --port-mapping) to the single-process layout, as
// identityMgmtArgs and identityHostdArgs do for the separate one (#457).
// Unset (state from before they were saved) means the daemons' defaults;
// an explicit 0 turns the pool off. Either pointer may be nil.
//
// Port mapping is process-wide: superviseSingle also calls
// netd.DisablePortMapping before mgmt's node exists.
func (c Config) identitySingle(mo *mgmtd.Options, ho *hostdrun.Options) {
	if mo != nil {
		mo.WarmKeys = mgmtd.DefaultWarmKeys
		if c.WarmKeys != nil {
			mo.WarmKeys = *c.WarmKeys
		}
	}
	if ho != nil {
		ho.PoolNodes = hostdrun.DefaultPoolNodes
		if c.PoolNodes != nil {
			ho.PoolNodes = *c.PoolNodes
		}
		ho.PortMapping = c.PortMapping
	}
}
