package hostd

// Tailnet HTTPS (tailworks.jsonc tailnet.https, #360, ADR-tailnet-https).
//
// With the setting on, the workload's entry node also serves :443 with its
// ts.net certificate (netd/tailnethttps.go), and a request that arrived
// that way reaches the Worker with an https:// request.url:
//
//	caller ─TLS─▶ workload node :443 (netd: WhoIs, strip, X-Forwarded-Proto: https)
//	       ─▶ ingressHandler: cf.tw.https = true (only for the workload node)
//	       ─▶ platform.js: request.url http:// → https:// ─▶ the Worker's fetch()
//
// Public (Funnel) requests are HTTPS too (Funnel terminates TLS), so they
// get cf.tw.https and an https:// request.url as well, with or without
// the setting. A Node-tier app gets X-Forwarded-Proto: https instead (its URL is the
// app's own business). Plain :80 is served as before.
//
// The deploy checks it: on a tailnet without HTTPS certificates the
// deployment fails with tailnet_https_unavailable before it replaces the
// active one, rather than coming up with an https:// URL that never
// answers. Only the workload node gets the listener: per-object nodes
// (facets) and Tailscale Service VIPs stay http:// (not needed for #360 yet).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/netd"
)

// tailnetHTTPSReadyWait bounds how long a deploy waits for the workload
// node before it can check the tailnet's certificates.
const tailnetHTTPSReadyWait = 90 * time.Second

// applyTailnetHTTPS opens (or closes) the workload node's :443 listener
// for app, before app's instance becomes active. A refusal fails the
// deployment (the active one, if any, keeps serving as it was).
func (h *Host) applyTailnetHTTPS(ctx context.Context, t *tenant, w *workload, wl *object, app *bundle.App) error {
	if t == nil || t.reg == nil || wl == nil {
		return nil
	}
	want := app.TailnetHTTPS
	if want && h.cfg.TailnetHTTPSOff {
		h.log.Info("tailnet.https ignored on this host (tw dev): the workload is served on http:// only", "workload", w.name)
		want = false
	}
	if !want {
		w.httpsOn.Store(false)
		return t.reg.SetTailnetHTTPS(wl.NetdID, false)
	}
	if err := h.waitReady(ctx, wl, tailnetHTTPSReadyWait); err != nil {
		return fmt.Errorf("tailnet.https: workload node: %w", err)
	}
	if err := t.reg.SetTailnetHTTPS(wl.NetdID, true); err != nil {
		if e, ok := netd.AsError(err); ok {
			return &apiError{Code: e.Code, Detail: "tailnet.https: " + e.Detail}
		}
		return fmt.Errorf("tailnet.https: %w", err)
	}
	w.httpsOn.Store(true)
	h.log.Info("tailnet https on", "workload", w.name, "node", wl.MagicDNS)
	return nil
}

// tailnetHTTPSLocked reports whether o's node starts with its :443
// listener: the workload node of a workload whose active deployment
// turned it on (a node restarted after control rejected it, #200, comes
// back with it). h.mu must be held.
func (h *Host) tailnetHTTPSLocked(o *object) bool {
	if o.Class != "" {
		return false
	}
	w := h.workloads[o.TenantID+"/"+o.Workload]
	return w != nil && w.httpsOn.Load()
}

// markHTTPSRoute tells the platform that a request reached the workload
// node over HTTPS (cf.tw.https), so the Worker sees an https://
// request.url: a public request (always: Funnel is HTTPS only), or a
// tailnet one on the :443 listener. netd strips every client
// X-Forwarded-Proto and sets it only for those two.
func markHTTPSRoute(r *http.Request, o *object, blob, route map[string]any) {
	if o.Class != "" {
		return
	}
	if publicBlob(blob) || r.Header.Get("X-Forwarded-Proto") == "https" {
		route["https"] = true
	}
}

// keepForwardedProto passes netd's X-Forwarded-Proto on to a Node-tier app
// (httputil.ReverseProxy's Rewrite drops it).
func keepForwardedProto(pr *httputil.ProxyRequest) {
	if v := pr.In.Header.Get("X-Forwarded-Proto"); v != "" {
		pr.Out.Header.Set("X-Forwarded-Proto", v)
	}
}
