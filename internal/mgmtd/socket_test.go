package mgmtd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestUnixSocketNeedsHostToken pins ledger claim C10 (RT-03) to the
// wiring: the unix socket Start serves with LocalDev refuses every request
// without the per-host token it minted, /v1/healthz included, and the
// token comes from the 0600 file Start wrote. mgmt.TestHostTokenOnUnixSocket
// and security/attacks/rt03 wrap a handler with mgmt.LocalHandler
// themselves; this one runs Start, which is what tw-mgmt and tw up run.
func TestUnixSocketNeedsHostToken(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "tw-mgmtd-") // short: unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "mgmt.sock")
	tokFile := filepath.Join(dir, "hostd", "mgmt.token")
	rt, err := Start(context.Background(), Options{
		StateDir:      filepath.Join(dir, "state"),
		LocalDev:      true,
		Socket:        sock,
		HostTokenFile: tokFile,
		NoLoopback:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	fi, err := os.Stat(tokFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	token := mgmt.ReadHostToken(tokFile)
	if len(token) != 64 {
		t.Fatalf("token %q", token)
	}

	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	get := func(path, tok string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://tw-mgmt"+path, nil)
		if tok != "" {
			req.Header.Set(mgmt.HostTokenHeader, tok)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var eb struct {
			Code string `json:"code"`
		}
		json.NewDecoder(res.Body).Decode(&eb)
		return res.StatusCode, eb.Code
	}
	for _, path := range []string{"/v1/healthz", "/v1/tenants", "/v1/hosts", "/v1/audit", "/mcp"} {
		if st, code := get(path, ""); st != 401 || code != "host_token_required" {
			t.Errorf("%s without a token: %d %s (want 401 host_token_required)", path, st, code)
		}
		if st, code := get(path, mgmt.NewHostToken()); st != 403 || code != "host_token_invalid" {
			t.Errorf("%s with a wrong token: %d %s (want 403 host_token_invalid)", path, st, code)
		}
	}
	if st, _ := get("/v1/healthz", token); st != 200 {
		t.Fatalf("healthz with the token from the file: %d", st)
	}
}
