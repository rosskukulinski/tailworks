package hostdrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd"
	"tailscale.com/tsnet"
)

// The host's internet path: -worker-internet and -container-internet
// (ADR-049, ADR-054). Each is one of
//
//	direct                       this host's network (public addresses only)
//	exit-node=<node>[,dns=<ip>]  through that tailnet exit node, via this host's own node
//	off                          refused
//
// A host has one internet path: when either flag is exit-node, the other
// must be exit-node (bare, or naming the same node) or off.
const (
	InternetDirect   = "direct"
	InternetOff      = "off"
	InternetExitNode = "exit-node"
)

// InternetModeHelp is the flag help both flags share.
const InternetModeHelp = "direct (this host's network, public addresses only), exit-node=<node>[,dns=<ip[:port]>] (through that tailnet exit node, via this host's own node) or off"

// ParseInternetMode splits a flag value into its kind (InternetDirect,
// InternetOff or InternetExitNode) and, for exit-node, the spec after "="
// ("" for bare exit-node). The spec is validated but not lowercased: node
// IDs are case-sensitive.
func ParseInternetMode(v string) (kind, spec string, err error) {
	v = strings.TrimSpace(v)
	k, spec, hasSpec := strings.Cut(v, "=")
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case InternetDirect, InternetOff:
		if hasSpec {
			return "", "", fmt.Errorf("%q: want %s", v, InternetModeHelp)
		}
		return k, "", nil
	case InternetExitNode:
		spec = strings.TrimSpace(spec)
		if hasSpec && spec == "" {
			return "", "", fmt.Errorf("%q: exit-node needs a node: want %s", v, InternetModeHelp)
		}
		if spec != "" {
			if _, err := hostd.ParseExitNodeSpec(spec); err != nil {
				return "", "", err
			}
		}
		return k, spec, nil
	}
	return "", "", fmt.Errorf("%q: want %s", v, InternetModeHelp)
}

// internetPaths validates both flags together and builds hostd's paths.
// exit is non-nil when the host's path is an exit node; close it with the
// runtime.
func internetPaths(ctx context.Context, worker, container string, ts *tsnet.Server, log *slog.Logger) (wk, ct *hostd.ContainerInternet, exit *hostd.ExitNodeInternet, err error) {
	wkKind, wkSpec, err := ParseInternetMode(worker)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("-worker-internet %w", err)
	}
	ctKind, ctSpec, err := ParseInternetMode(container)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("-container-internet %w", err)
	}
	if wkKind != InternetExitNode && ctKind != InternetExitNode {
		if wkKind == InternetDirect {
			wk = hostd.DirectInternet()
		}
		if ctKind == InternetDirect {
			ct = hostd.DirectInternet()
		}
		return wk, ct, nil, nil
	}
	// One of them is exit-node: a host has one internet path.
	if wkKind == InternetDirect || ctKind == InternetDirect {
		return nil, nil, nil, errors.New("a host has one internet path: with exit-node, -worker-internet and -container-internet must both be exit-node (or off)")
	}
	spec := wkSpec
	switch {
	case spec == "":
		spec = ctSpec
	case ctSpec != "" && ctSpec != wkSpec:
		return nil, nil, nil, fmt.Errorf("-worker-internet names exit node %q and -container-internet %q: a host has one exit node", wkSpec, ctSpec)
	}
	if spec == "" {
		return nil, nil, nil, errors.New("exit-node needs a node: -worker-internet=exit-node=<node>")
	}
	if ts == nil {
		return nil, nil, nil, errors.New("exit-node needs this host's own tailnet node (the tsnet transport; not -local-dev or an embedded host)")
	}
	sp, err := hostd.ParseExitNodeSpec(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	if exit, err = hostd.NewExitNodeInternet(ctx, ts, sp, log); err != nil {
		return nil, nil, nil, err
	}
	if wkKind == InternetExitNode {
		wk = exit.Internet()
	}
	if ctKind == InternetExitNode {
		ct = exit.Internet()
	}
	return wk, ct, exit, nil
}
