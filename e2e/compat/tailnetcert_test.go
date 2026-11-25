//go:build e2ecompat

package compat

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// tailnetHTTPSCertWait bounds waitTailnetCert: a new node name's first
// ts.net certificate takes 30–100 s, sometimes minutes
// (netd/certprefetch.go).
const tailnetHTTPSCertWait = 8 * time.Minute

// waitTailnetCert waits until a TLS handshake with the workload's :443
// listener completes for name, i.e. its certificate has been issued, so
// the probe's 60 s request timeout measures the app and not certificate
// issuance (#614). It returns how long that took.
func waitTailnetCert(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), ip, name string) (time.Duration, error) {
	name = strings.TrimSuffix(name, ".")
	st := time.Now()
	ctx, cancel := context.WithTimeout(ctx, tailnetHTTPSCertWait)
	defer cancel()
	var last error
	for {
		actx, acancel := context.WithTimeout(ctx, 30*time.Second)
		c, err := dial(actx, "tcp", net.JoinHostPort(ip, "443"))
		if err == nil {
			tc := tls.Client(c, &tls.Config{ServerName: name})
			err = tc.HandshakeContext(actx)
			tc.Close()
		}
		acancel()
		if err == nil {
			return time.Since(st), nil
		}
		last = err
		select {
		case <-ctx.Done():
			// Seen on tailnet 1 (#614): the LocalAPI cert request of any
			// new name, even the client node's own, times out. That is
			// certificate issuance for the tailnet, not the app.
			return time.Since(st), fmt.Errorf("the tailnet issued no HTTPS certificate for %s within %s (certificate issuance for the tailnet, not the app; last handshake error: %v)", name, tailnetHTTPSCertWait, last)
		case <-time.After(2 * time.Second):
		}
	}
}
