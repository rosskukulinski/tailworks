package hostd

import (
	"strconv"
	"strings"
)

// Process-tier memory caps (issue #115; docs/limits.md "Process tier").
// The only per-workload memory bound tailworks can set reliably on both
// Linux and macOS is V8's own heap limit: RLIMIT_AS breaks V8 (it reserves
// far more address space than it uses), macOS enforces no address-space or
// data rlimit, and a cgroup needs a delegated subtree from the service
// manager. The defaults are far above what a Worker or a typical Node
// server holds; they exist so that one runaway workload can't take every
// tenant's memory on the host.
const (
	// DefaultWorkerdHeapMiB caps each V8 isolate in a process-tier workerd
	// (the Worker and each Durable Object class). Cloudflare's is 128 MB.
	DefaultWorkerdHeapMiB = 1024
	// DefaultNodeHeapMiB caps a Node-tier app's V8 heap
	// (--max-old-space-size), unless the app sets its own in node.args or
	// NODE_OPTIONS.
	DefaultNodeHeapMiB = 2048
)

// heapMiB is v, DefaultX for 0, and 0 (no cap) when negative.
func heapMiB(v, def int) int {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return v
}

func (h *Host) workerdHeapMiB() int { return heapMiB(h.cfg.WorkerdHeapMiB, DefaultWorkerdHeapMiB) }

const nodeHeapFlag = "--max-old-space-size"

// nodeHeapArgs prepends the heap cap to a Node app's arguments, unless the
// app already sets one (its node.args or NODE_OPTIONS win) or mib is 0.
func nodeHeapArgs(args []string, env map[string]string, mib int) []string {
	if mib <= 0 {
		return args
	}
	has := func(s string) bool {
		return s == nodeHeapFlag || strings.HasPrefix(s, nodeHeapFlag+"=") ||
			s == "--max_old_space_size" || strings.HasPrefix(s, "--max_old_space_size=")
	}
	for _, a := range args {
		if has(a) {
			return args
		}
	}
	for _, a := range strings.Fields(env["NODE_OPTIONS"]) {
		if has(a) {
			return args
		}
	}
	return append([]string{nodeHeapFlag + "=" + strconv.Itoa(mib)}, args...)
}
