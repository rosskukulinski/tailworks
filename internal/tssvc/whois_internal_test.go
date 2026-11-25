package tssvc

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestDialedAddr(t *testing.T) {
	for _, tc := range []struct {
		local net.Addr
		want  string
	}{
		{&net.TCPAddr{IP: net.ParseIP("100.99.0.12"), Port: 80}, "100.99.0.12"},
		{&net.TCPAddr{IP: net.ParseIP("fd7a:115c:a1e0::1"), Port: 80}, "fd7a:115c:a1e0::1"},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:100.99.0.12"), Port: 80}, "100.99.0.12"},
		{&net.TCPAddr{IP: net.IPv4zero, Port: 80}, ""},
		{&net.UnixAddr{Name: "/tmp/x.sock", Net: "unix"}, ""},
		{nil, ""},
	} {
		ctx := context.Background()
		if tc.local != nil {
			ctx = context.WithValue(ctx, http.LocalAddrContextKey, tc.local)
		}
		got, ok := dialedAddr(ctx)
		if tc.want == "" {
			if ok {
				t.Errorf("%v: got %v, want no address", tc.local, got)
			}
			continue
		}
		if !ok || got.String() != tc.want {
			t.Errorf("%v: got %v %v, want %s", tc.local, got, ok, tc.want)
		}
	}
}
