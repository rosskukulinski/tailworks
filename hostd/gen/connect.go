package gen

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Raw TCP egress (ADR-052). The platform's connect() handlers open
// an HTTP CONNECT tunnel to hostd's egress endpoints for the destination
// host:port. workerd sends CONNECT with no headers (not even
// injectRequestHeaders), so the tunnel carries its own authentication: once
// hostd has answered 200, the platform writes one ConnectPreamble line
// (JSON, "\n"-terminated), hostd checks it, dials, and answers one
// ConnectStatus line before the tunnel carries the flow's bytes.

// MaxConnectLine bounds a preamble or status line.
const MaxConnectLine = 4096

// ConnectPreamble is the first line the platform writes on a tunnel.
type ConnectPreamble struct {
	V int `json:"v"`
	// Token is the instance's loopback token (Options.Loopback); empty
	// on unix sockets and in a microVM.
	Token string `json:"token,omitempty"`
	// A facet object's identity, MAC'd like a facet's fetch() egress
	// (ComputeMAC with method "CONNECT" and the destination as the URL).
	// Name is URL-escaped (encodeURIComponent), as in the HTTP headers.
	Class string `json:"class,omitempty"`
	Name  string `json:"name,omitempty"`
	TS    string `json:"ts,omitempty"`
	Nonce string `json:"nonce,omitempty"`
	MAC   string `json:"mac,omitempty"`
	// TLS asks hostd to originate TLS to the destination
	// (ctx.tailnet.connect({ tls: "netd" })); the tunnel then carries
	// plaintext between the Worker and hostd.
	TLS bool `json:"tls,omitempty"`
}

// ConnectStatus is hostd's answer to a preamble.
type ConnectStatus struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ReadConnectLine reads one "\n"-terminated JSON line of at most
// MaxConnectLine bytes into v.
func ReadConnectLine(br *bufio.Reader, v any) error {
	var line []byte
	for {
		b, err := br.ReadSlice('\n')
		line = append(line, b...)
		if len(line) > MaxConnectLine {
			return errors.New("connect: line too long")
		}
		if err == nil {
			break
		}
		if err != bufio.ErrBufferFull {
			return err
		}
	}
	return json.Unmarshal(bytes.TrimSpace(line), v)
}

// WriteConnectLine writes v as one JSON line.
func WriteConnectLine(w interface{ Write([]byte) (int, error) }, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ConnectTokenOK reports whether p carries want (constant time). An empty
// want (no loopback listener) accepts anything.
func ConnectTokenOK(p *ConnectPreamble, want string) bool {
	if want == "" {
		return true
	}
	return hmac.Equal([]byte(p.Token), []byte(want))
}

// VerifyConnectOnce checks a facet tunnel's preamble MAC for dst, with the
// replay check of VerifyEgressOnce, and returns the object's class and name.
func VerifyConnectOnce(root string, p *ConnectPreamble, dst string, now time.Time, seen *NonceCache) (class, name string, ok bool) {
	h := http.Header{}
	h.Set(HeaderClass, p.Class)
	h.Set(HeaderName, p.Name)
	h.Set(HeaderTS, p.TS)
	h.Set(HeaderNonce, p.Nonce)
	h.Set(HeaderMAC, p.MAC)
	return VerifyEgressOnce(root, h, http.MethodConnect, dst, now, seen)
}
