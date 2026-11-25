//go:build e2emultimgmt

package multimgmt

import (
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/chaoskit"
)

// pgProxy sits between one replica and Postgres and injects faults:
// pass, delay, blackhole and reset (chaoskit.Proxy, which e2e/dataplane
// also uses in front of its S3 backend).
type pgProxy struct{ p *chaoskit.Proxy }

func newPGProxy(t *testing.T, target string) *pgProxy {
	p, err := chaoskit.Listen(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return &pgProxy{p}
}

func (p *pgProxy) addr() string { return p.p.Addr() }

// set changes the mode; "reset" also drops every open connection.
func (p *pgProxy) set(mode string, d time.Duration) { p.p.Set(mode, d) }

// viaProxy rewrites a postgres:// DSN to go through addr.
func viaProxy(t *testing.T, dsn, addr string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = addr
	out := u.String()
	if extra := os.Getenv("TW_MM_DSN_SUFFIX"); extra != "" { // e.g. &tw_max_conns=2
		out += extra
	}
	return out
}
