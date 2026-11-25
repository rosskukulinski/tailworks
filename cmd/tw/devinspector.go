package main

// tw dev --inspector-port (issue #42): Chrome DevTools or VS Code attach
// to 127.0.0.1:<port>, as with `wrangler dev --inspector-port`. hostd
// starts every workerd with its own inspector port (hostd/inspector.go):
// a reload starts the new workerd before the old one stops, so the fixed
// port is this proxy, which forwards to the active workerd's. It rewrites
// workerd's /json discovery answers to point at itself, and passes the
// DevTools WebSocket through (httputil.ReverseProxy handles the upgrade).
// devinspectorguard.go decides who may connect and which targets exist.

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

// devInspector serves the inspector proxy on 127.0.0.1:port; target is the
// active workerd's inspector address ("" while none runs).
func devInspector(port int, target func() string) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, "", fail("bad_request", fmt.Sprintf("can't listen on 127.0.0.1:%d for the inspector", port), err.Error()).
			withFix(fmt.Sprintf("tw dev --inspector-port %d", port+1))
	}
	outer := ln.Addr().String()
	srv := &http.Server{Handler: devInspectorHandler(outer, target), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return srv, outer, nil
}

func devInspectorHandler(outer string, target func() string) http.Handler {
	rp := &httputil.ReverseProxy{
		ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			t := target()
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "http", t, t
		},
		ModifyResponse: func(r *http.Response) error {
			if !strings.HasPrefix(r.Request.URL.Path, "/json") {
				return nil
			}
			b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body.Close()
			if err != nil {
				return err
			}
			b = rewriteInspectorJSON(b, r.Request.URL.Host, outer)
			if p := r.Request.URL.Path; p == "/json" || p == "/json/list" {
				b = filterInspectorTargets(b, outer) // devinspectorguard.go
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			r.Header.Set("Content-Length", strconv.Itoa(len(b)))
			return nil
		},
	}
	return devInspectorGuard(outer, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target() == "" {
			http.Error(w, "tw dev: no workerd is running yet", http.StatusServiceUnavailable)
			return
		}
		rp.ServeHTTP(w, r)
	}))
}

// rewriteInspectorJSON points workerd's /json answers (webSocketDebuggerUrl,
// devtoolsFrontendUrl) at the proxy instead of the workerd's own port,
// which changes on every reload.
func rewriteInspectorJSON(b []byte, inner, outer string) []byte {
	if inner == "" {
		return b
	}
	b = bytes.ReplaceAll(b, []byte(inner), []byte(outer))
	if _, p, err := net.SplitHostPort(inner); err == nil {
		b = bytes.ReplaceAll(b, []byte("localhost:"+p), []byte(outer))
	}
	return b
}
