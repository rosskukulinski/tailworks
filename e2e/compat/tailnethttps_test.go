package compat

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// A TailnetHTTPS project is called at https://<MagicDNS name> (the
// certificate's name), every other one at http://<IP>; both dial the IP,
// on the URL's port.
func TestDeployedBase(t *testing.T) {
	plain, tls := &Project{}, &Project{TailnetHTTPS: true}
	if got := deployedBase(plain, "100.64.0.9", "app.t.ts.net."); got != "http://100.64.0.9" {
		t.Errorf("plain: %s", got)
	}
	if got := deployedBase(tls, "100.64.0.9", "app.t.ts.net."); got != "https://app.t.ts.net" {
		t.Errorf("tailnet https: %s", got)
	}
	var dialled string
	stop := errors.New("stop")
	hc := deployedClient(func(_ context.Context, _, addr string) (net.Conn, error) {
		dialled = addr
		return nil, stop
	}, "100.64.0.9", time.Second)
	hc.Get("https://app.t.ts.net/mcp")
	if dialled != "100.64.0.9:443" {
		t.Errorf("https://app.t.ts.net dialled %q, want the IP on 443", dialled)
	}
	hc.Get("http://100.64.0.9/")
	if dialled != "100.64.0.9:80" {
		t.Errorf("http://<ip> dialled %q", dialled)
	}
}
