package main

// Who may use the tw dev inspector proxy (issue #335, ledger
// H-dev-inspector). The inspector runs code in the dev workload's isolate,
// so a web page open in the developer's browser must not reach it:
//
//   - Host must name the proxy's loopback address (127.0.0.1, localhost or
//     [::1] with its port), else 421. A page on a DNS name rebound to
//     127.0.0.1 sends its own name, so it can't read /json.
//   - Origin, when present, must be a debugger's (devtools://,
//     chrome-devtools://, vscode-webview://, vscode-file://) or the proxy's
//     own loopback origin, else 403. A WebSocket isn't held back by the
//     same-origin policy; Origin is what tells a page's upgrade apart.
//     Chrome's chrome://inspect discovery and VS Code's js-debug send none.
//   - Only the tenant isolate is a target. workerd names its targets after
//     the config's services (hostd/gen/gen.go): platform, deny, workflows
//     and assets are tailworks' own isolates, and platform holds MAC_KEY.
//     They are dropped from /json and their paths are refused (403).
//
// workerd's own inspector port, which the proxy forwards to, checks none of
// this; docs/dev-debugging.md has the residual.

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// devInspectorTarget is the one debuggable workerd service: the app.
const devInspectorTarget = "tenant"

// devInspectorOrigins are the Origin schemes of debugger front ends.
var devInspectorOrigins = map[string]bool{
	"chrome-devtools": true,
	"devtools":        true,
	"vscode-file":     true,
	"vscode-webview":  true,
}

// devInspectorHosts returns the Host values that name the proxy listening
// on outer (127.0.0.1:port), lowercased.
func devInspectorHosts(outer string) map[string]bool {
	_, port, _ := net.SplitHostPort(outer)
	hosts := map[string]bool{}
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]"} {
		hosts[h+":"+port] = true
	}
	return hosts
}

// devInspectorGuard wraps next with the Host, Origin and target checks.
func devInspectorGuard(outer string, next http.Handler) http.Handler {
	hosts := devInspectorHosts(outer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "tw dev inspector: connect to "+outer+" (DNS-rebinding guard)", http.StatusMisdirectedRequest)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !devInspectorOriginOK(o, hosts) {
			http.Error(w, "tw dev inspector: Origin "+o+" may not attach; use Chrome DevTools or VS Code", http.StatusForbidden)
			return
		}
		if !devInspectorPathOK(r.URL.Path) {
			http.Error(w, "tw dev inspector: only the "+devInspectorTarget+" target is debuggable", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// devInspectorOriginOK reports whether origin is a debugger front end or
// the proxy's own loopback origin.
func devInspectorOriginOK(origin string, hosts map[string]bool) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	s := strings.ToLower(u.Scheme)
	if devInspectorOrigins[s] {
		return true
	}
	return (s == "http" || s == "https") && u.Path == "" && hosts[strings.ToLower(u.Host)]
}

// devInspectorPathOK reports whether path is discovery (/json…) or the
// tenant target's WebSocket.
func devInspectorPathOK(path string) bool {
	return path == "/json" || strings.HasPrefix(path, "/json/") || path == "/"+devInspectorTarget
}

// filterInspectorTargets keeps only the tenant target in a /json or
// /json/list answer (already rewritten to point at outer) and points its
// devtoolsFrontendUrl at the target: workerd's names no path. An answer
// that isn't a target list becomes an empty list (fail closed).
func filterInspectorTargets(b []byte, outer string) []byte {
	var targets []map[string]any
	if err := json.Unmarshal(b, &targets); err != nil {
		return []byte("[]")
	}
	kept := []map[string]any{}
	for _, t := range targets {
		if t["id"] != devInspectorTarget {
			continue
		}
		t["webSocketDebuggerUrl"] = "ws://" + outer + "/" + devInspectorTarget
		for _, k := range []string{"devtoolsFrontendUrl", "devtoolsFrontendUrlCompat"} {
			if s, ok := t[k].(string); ok && strings.HasSuffix(s, "ws="+outer+"/") {
				t[k] = s + devInspectorTarget
			}
		}
		kept = append(kept, t)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // keep the URLs' & as workerd wrote them
	if err := enc.Encode(kept); err != nil {
		return []byte("[]")
	}
	return out.Bytes()
}
