package compat

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// deployedBase is where the deployed run calls p: http://<workload IP>,
// or https://<MagicDNS name> for a TailnetHTTPS project (#360), whose
// certificate is for that name.
func deployedBase(p *Project, ip, magicDNS string) string {
	if p.TailnetHTTPS {
		return "https://" + strings.TrimSuffix(magicDNS, ".")
	}
	return "http://" + ip
}

// deployedClient is the probe's HTTP client: every connection goes to the
// workload's IP over the tailnet (dial), on the URL's port, so an
// https://<name> URL needs no MagicDNS lookup on the client.
func deployedClient(dial func(ctx context.Context, network, addr string) (net.Conn, error), ip string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return dial(ctx, network, net.JoinHostPort(ip, port))
		},
		Proxy:               nil,
		MaxIdleConnsPerHost: 8,
	}}
}
