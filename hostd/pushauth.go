package hostd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// MgmtTag is mgmt's own node tag (the same value as mgmt.MgmtTag; hostd
// does not import mgmt). Only a node carrying it may push to a host.
const MgmtTag = "tag:tw-mgmt"

// WhoIsFunc resolves a tailnet peer by its remote address (tsnet
// LocalClient.WhoIs).
type WhoIsFunc func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)

// StatusFunc reports this node's netmap (tsnet LocalClient.Status); hostd
// uses it to find mgmt's node when -mgmt names it by MagicDNS.
type StatusFunc func(ctx context.Context) (*ipnstate.Status, error)

// pushRelookupEvery bounds how often a push from an unexpected node makes
// hostd re-resolve mgmt's address (a recreated mgmt node has a new ID).
const pushRelookupEvery = 2 * time.Second

// pushAuth authenticates the push API served on the host's own tsnet node
// (H1): the peer must be a tailnet node carrying MgmtTag, and once mgmt's
// node is known (resolved from the -mgmt address at Start, else the first
// tagged pusher) every push must come from that stable node ID.
type pushAuth struct {
	mgmtHost string // host part of the -mgmt URL: a tailnet IP or MagicDNS name
	whois    WhoIsFunc
	status   StatusFunc
	log      *slog.Logger

	mu         sync.Mutex
	pinned     tailcfg.StableNodeID
	lastLookup time.Time
}

func newPushAuth(mgmtBase string, whois WhoIsFunc, status StatusFunc, log *slog.Logger) *pushAuth {
	host := ""
	if u, err := url.Parse(mgmtBase); err == nil {
		host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	}
	return &pushAuth{mgmtHost: host, whois: whois, status: status, log: log}
}

// pin resolves mgmt's node from the -mgmt address and records its stable
// ID. It fails (without pinning) when the address does not resolve to a
// peer in this node's netmap or that peer does not carry MgmtTag.
func (a *pushAuth) pin(ctx context.Context) error {
	id, err := a.lookup(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.pinned = id
	a.mu.Unlock()
	return nil
}

// Pinned is the stable node ID pushes are pinned to ("" until known).
func (a *pushAuth) Pinned() tailcfg.StableNodeID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pinned
}

// lookup resolves the -mgmt host to a tagged peer's stable node ID.
func (a *pushAuth) lookup(ctx context.Context) (tailcfg.StableNodeID, error) {
	if a.mgmtHost == "" {
		return "", errors.New("no mgmt host to resolve")
	}
	if net.ParseIP(a.mgmtHost) != nil {
		// tw up passes mgmt's tailnet IP: WhoIs resolves it directly.
		if a.whois == nil {
			return "", errors.New("no WhoIs")
		}
		who, err := a.whois(ctx, a.mgmtHost)
		if err != nil {
			return "", fmt.Errorf("whois %s: %w", a.mgmtHost, err)
		}
		if who == nil || who.Node == nil {
			return "", fmt.Errorf("whois %s: no node", a.mgmtHost)
		}
		if !slices.Contains(who.Node.Tags, MgmtTag) {
			return "", fmt.Errorf("mgmt node %s (%s) does not carry %s (tags %v)", a.mgmtHost, who.Node.StableID, MgmtTag, who.Node.Tags)
		}
		return who.Node.StableID, nil
	}
	if a.status == nil {
		return "", errors.New("no netmap status to resolve " + a.mgmtHost)
	}
	st, err := a.status(ctx)
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	if st == nil {
		return "", errors.New("status: nil")
	}
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
		first, _, _ := strings.Cut(name, ".")
		if name != a.mgmtHost && !(first == a.mgmtHost && !strings.Contains(a.mgmtHost, ".")) {
			continue
		}
		if p.Tags == nil || !slices.Contains(p.Tags.AsSlice(), MgmtTag) {
			return "", fmt.Errorf("mgmt node %s (%s) does not carry %s", name, p.ID, MgmtTag)
		}
		return p.ID, nil
	}
	return "", fmt.Errorf("%s is not a peer in this node's netmap", a.mgmtHost)
}

// handler wraps next: every request must come from mgmt's node.
func (a *pushAuth) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.check(r.Context(), r.RemoteAddr); err != nil {
			writeAPIError(w, &apiError{Code: "push_unauthorized", Detail: err.Error()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// check authenticates one push from remoteAddr. Refusals are logged with
// the peer's address and tags (never key material) and return the detail
// sent to the caller.
func (a *pushAuth) check(ctx context.Context, remoteAddr string) error {
	refuse := func(tags []string, why string) error {
		a.log.Warn("push refused", "peer", remoteAddr, "tags", tags, "reason", why)
		return errors.New(why)
	}
	if a.whois == nil {
		return refuse(nil, "push API has no WhoIs authenticator")
	}
	who, err := a.whois(ctx, remoteAddr)
	if err != nil || who == nil || who.Node == nil {
		return refuse(nil, "peer has no tailnet identity")
	}
	tags := who.Node.Tags
	if !slices.Contains(tags, MgmtTag) {
		return refuse(tags, "peer does not carry "+MgmtTag)
	}
	id := who.Node.StableID
	if id == "" {
		return refuse(tags, "peer has no stable node ID")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pinned == "" {
		// Not resolvable at Start (mgmt named by MagicDNS before the peer
		// reached the netmap): the first tagged pusher is mgmt.
		a.pinned = id
		a.log.Info("push API pinned to mgmt's node", "node", id)
		return nil
	}
	if a.pinned == id {
		return nil
	}
	// Another tag:tw-mgmt node. Only legitimate if mgmt's node was
	// recreated: re-resolve the -mgmt address (rate-limited) and re-pin
	// when it now names the caller.
	if time.Since(a.lastLookup) >= pushRelookupEvery {
		a.lastLookup = time.Now()
		if cur, err := a.lookup(ctx); err == nil && cur == id {
			a.log.Info("push API re-pinned to mgmt's node", "old", a.pinned, "node", id)
			a.pinned = id
			return nil
		}
	}
	return refuse(tags, fmt.Sprintf("peer %s is not mgmt's node (%s)", id, a.pinned))
}
