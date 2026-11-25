package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"golang.org/x/term"
)

// publicHTTP resolves through public DNS (1.1.1.1, then the system
// resolver): a machine on another tailnet may not resolve *.ts.net Funnel
// names through its own MagicDNS.
func publicHTTP() *http.Client {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "udp", "1.1.1.1:53")
	}}
	d := &net.Dialer{Timeout: 10 * time.Second, Resolver: r}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			c, err = (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
		}
		return c, err
	}
	return &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
}

// isTerminal reports whether stdin is a terminal (for prompts).
func isTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// oauthAPI is a Tailscale API client authenticated by this state's OAuth
// client (nil unless the state is in oauth mint mode). What it may do is
// the client's scopes' business.
func (s *state) oauthAPI() *tsapi.Client {
	if s == nil || !s.Cfg.oauth() || s.Cfg.OAuthClientFile == "" {
		return nil
	}
	file := s.Cfg.OAuthClientFile
	src := &tsapi.OAuthSource{BaseURL: s.Cfg.TSAPIURL, Load: func() (tsapi.OAuthCredentials, error) { return tsapi.LoadOAuthFile(file) }}
	return &tsapi.Client{Tailnet: s.Cfg.Tailnet, Tokens: src, BaseURL: s.Cfg.TSAPIURL}
}
