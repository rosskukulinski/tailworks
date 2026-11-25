// Package compatflags classifies workerd compatibility flags: which of the
// flags a tenant's wrangler config lists does workerd mark experimental?
//
// Every tailworks workerd runs with --experimental because the platform
// worker needs it (the worker loader, facets). Without the switch workerd
// refuses an experimental flag: "The compatibility flag X is experimental
// … you must pass --experimental". So the switch, which is there for the
// platform, would also unlock every experimental flag for tenant code
// (unsafe_module, rtti_api, precise_timers, …; issue #337, ledger
// H-compat-flags). hostd/gen refuses them at deploy unless the operator
// allows them (tw-hostd -allow-experimental-flags), and mgmt refuses them
// early when no host allows them (internal/features ExperimentalFlags).
//
// Two sources, used together:
//
//   - Experimental, the pinned workerd's list, copied from upstream's
//     src/workerd/io/compatibility-date.capnp ($experimental fields). mgmt
//     and the CLI have no workerd binary, so they use this list.
//     TestExperimentalMatchesUpstream fails when it drifts from the capnp of
//     version.WorkerdPin, and TestExperimentalRefusedByWorkerd when the
//     pinned binary disagrees with it.
//   - Probe, which asks the pinned workerd itself: `workerd test` over a
//     throwaway config that carries the flags, without --experimental,
//     exits in ~20 ms naming every experimental flag. hostd runs it at
//     deploy (cached per binary and flag set), so a flag that a workerd
//     bump adds is refused even before the list is updated.
package compatflags

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Experimental is every compatibility flag workerd 1.20261121.1 marks
// $experimental (one per line, sorted; the `experimental` flag itself is
// one). Update it with the workerd pin: `make test` runs
// TestExperimentalMatchesUpstream, which prints the diff.
var Experimental = []string{
	"allow_insecure_inefficient_logged_eval",
	"auto_inject_python_workers",
	"cache_reload_enabled",
	"connect_handler_immediate_tls",
	"connect_handler_starttls",
	"connect_pass_through",
	"d1_binding_jsrpc",
	"durable_object_get_existing",
	"durable_object_rename",
	"durable_object_snapshots",
	"enable_abortsignal_rpc",
	"enable_ctx_version_metadata",
	"enable_d1_with_sessions_api",
	"enable_nodejs_inspector_local_dev",
	"enable_version_api",
	"enable_web_file_system",
	"experimental",
	"expose_draining_reader",
	"expose_serialization_test_class",
	"increase_websocket_message_size",
	"js_rpc",
	"kv_direct_binding",
	"memory_cache_delete",
	"offpeak_location_hint",
	"precise_timers",
	"python_workers_development",
	"python_workers_durable_objects",
	"python_zstd_snapshot",
	"r2_binding_jsrpc",
	"replica_routing",
	"rpc_dispose_cancels_call",
	"rtti_api",
	"service_binding_extra_handlers",
	"spec_compliant_property_attributes",
	"streaming_tail_worker",
	"streams_no_default_auto_allocate_chunk_size",
	"tail_worker_user_spans",
	"typescript_implemented_streams",
	"typescript_strip_types",
	"unsafe_module",
	"unsupported_process_actual_platform",
	"wasm_memory_discard",
	"webgpu",
	"workflows_bindings_rpc",
	"workflows_step_rollback",
}

// IsExperimental reports whether Experimental lists flag.
func IsExperimental(flag string) bool {
	_, ok := slices.BinarySearch(Experimental, flag)
	return ok
}

// Filter returns the flags in flags that Experimental lists, sorted and
// without duplicates (nil when none).
func Filter(flags []string) []string {
	var out []string
	for _, f := range flags {
		if IsExperimental(f) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// InBundle returns Filter of the compatibility flags a deploy bundle's
// wrangler config lists. A bundle that doesn't parse lists none (deploy's
// own validation reports it).
func InBundle(b []byte) []string {
	files, err := bundle.Unpack(b)
	if err != nil {
		return nil
	}
	app, err := bundle.Parse(files)
	if err != nil || app == nil {
		return nil
	}
	return Filter(app.CompatFlags)
}

// flagNameRE is what hostd/gen accepts as a flag name; Probe writes flags
// into a capnp config, so it accepts nothing else.
var flagNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// experimentalRE matches workerd's refusal of an experimental flag.
var experimentalRE = regexp.MustCompile(`The compatibility flag ([a-z0-9_]+) is experimental`)

// ProbeTimeout bounds one workerd run (it takes ~20 ms).
const ProbeTimeout = 15 * time.Second

var probeCache sync.Map // workerdBin + "\x00" + flags → []string

// Probe asks the workerd at workerdBin which of flags are experimental: it
// runs `workerd test` over a throwaway config with the flags, without
// --experimental, and parses the refusals. The result is cached per
// (binary, flag set) for the process's lifetime. Flags workerd doesn't
// know, or other config errors, are not reported here (the real start
// reports them, as before). An error means workerd couldn't be run.
func Probe(workerdBin string, flags []string) ([]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	sorted := slices.Clone(flags)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	for _, f := range sorted {
		if !flagNameRE.MatchString(f) {
			return nil, fmt.Errorf("compatflags: bad flag name %q", f)
		}
	}
	key := workerdBin + "\x00" + strings.Join(sorted, ",")
	if v, ok := probeCache.Load(key); ok {
		return v.([]string), nil
	}
	exp, err := probe(workerdBin, sorted)
	if err != nil {
		return nil, err
	}
	probeCache.Store(key, exp)
	return exp, nil
}

func probe(workerdBin string, flags []string) ([]string, error) {
	dir, err := os.MkdirTemp("", "tw-compatflags-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var c strings.Builder
	c.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\n")
	c.WriteString("const config :Workerd.Config = ( services = [ (name = \"probe\", worker = .probe) ] );\n")
	c.WriteString("const probe :Workerd.Worker = (\n  compatibilityDate = \"2000-01-01\",\n  compatibilityFlags = [")
	for i, f := range flags {
		if i > 0 {
			c.WriteString(", ")
		}
		c.WriteString("\"" + f + "\"")
	}
	c.WriteString("],\n  modules = [ (name = \"m.js\", esModule = \"export default {}\") ],\n);\n")
	cfg := filepath.Join(dir, "config.capnp")
	if err := os.WriteFile(cfg, []byte(c.String()), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, workerdBin, "test", cfg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return nil, fmt.Errorf("compatflags: run %s: %w", workerdBin, err)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("compatflags: %s did not finish in %s", workerdBin, ProbeTimeout)
	}
	// Exit status 1 is expected: the flags are refused, or (all stable)
	// "No tests found!". Only the refusals matter.
	var exp []string
	for _, m := range experimentalRE.FindAllStringSubmatch(string(out), -1) {
		if slices.Contains(flags, m[1]) {
			exp = append(exp, m[1])
		}
	}
	slices.Sort(exp)
	return slices.Compact(exp), nil
}
