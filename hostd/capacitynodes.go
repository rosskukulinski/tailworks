package hostd

// The node dimension of the capacity vector: how many object nodes a host
// advertises to placement when -capacity-nodes is 0 (the default). It is
// derived from RAM with the per-node memory BenchmarkRegistryNodes measured
// (`make bench`; docs/limits.md "Hosts", issue #114).
const (
	// NodeMemMiB is the RAM budgeted per online object node: the measured
	// per-node RSS under request load with scoped peer visibility (each
	// object sees only its callers), rounded up for headroom.
	NodeMemMiB = 8
	// nodeMemShare of the host's RAM is budgeted for object nodes; the
	// rest is for workerd, hostd, guests and the page cache.
	nodeMemShare = 0.25
	// MinCapacityNodes and MaxCapacityNodes bound the derived value.
	// The ceiling is the pre-measurement default: past it, rejoin after
	// a restart (one host's nodes come back in priority order, RFC §9)
	// and netmap size rather than RAM are the limit.
	MinCapacityNodes = 50
	MaxCapacityNodes = 1000
	// FallbackCapacityNodes is used when RAM can't be read.
	FallbackCapacityNodes = 500
)

// DefaultCapacityNodes is the node capacity for a host with totalMiB of RAM:
// nodeMemShare of it at NodeMemMiB per node, within [MinCapacityNodes,
// MaxCapacityNodes]. totalMiB ≤ 0 (unknown) gives FallbackCapacityNodes.
func DefaultCapacityNodes(totalMiB int) int {
	if totalMiB <= 0 {
		return FallbackCapacityNodes
	}
	n := int(float64(totalMiB) * nodeMemShare / NodeMemMiB)
	return min(max(n, MinCapacityNodes), MaxCapacityNodes)
}
