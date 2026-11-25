//go:build e2etwupgrade

package twupgrade

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// Time budgets for `tw up` (single process, to ready) and `tw down` on
// testcontrol (#245). Measured after the fix: up 0.3–0.7 s, down 0.1–0.3 s
// (macOS and a Linux VM). Before it: down 5.0 s every time (mgmt's node
// waited out its LocalAPI's 5 s shutdown grace for the HTTPS certificate
// prefetch), up 3.4–6.5 s on Linux (tw up's join and mgmt's start waited
// for netcheck and DERP). Since mgmt joins its own node (tw up hands it
// the key in a file): up 0.05–0.13 s from tw up's start to ready on
// macOS, 0.35–0.45 s before. The budgets leave room for a loaded machine.
const (
	upBudget   = 2 * time.Second
	downBudget = 2500 * time.Millisecond
)

// tsnetStartsPerUp: on production control every tsnet start costs about
// a second of round trips (GET /key, register, map), so `tw up` in the
// single-process layout starts exactly one, mgmt's own node. tw up used
// to join mgmt's node itself first and then close it: two.
const tsnetStartsPerUp = 1

// keyCounter is a reverse proxy to testcontrol that counts GET /key,
// which every tsnet start (a new control client) sends first.
type keyCounter struct {
	rp *httputil.ReverseProxy
	n  atomic.Int64
}

func (k *keyCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/key" {
		k.n.Add(1)
	}
	k.rp.ServeHTTP(w, r)
}

// TestUpDownBudget (#245): `tw up`, `tw down`, and `tw up` again on the
// saved state (what a service restart or a reboot runs), three times, each
// within its budget; the last down purges. Run it with `make e2e-tw-upgrade`.
func TestUpDownBudget(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	root, err := nettest.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildBins(t, root, "0.0.9")
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	cu, _ := url.Parse(ctl.URL)
	kc := &keyCounter{rp: httputil.NewSingleHostReverseProxy(cu)}
	proxy := httptest.NewServer(kc)
	t.Cleanup(proxy.Close)
	api := newFakeAPI(t, "v0.0.9")
	state, err := hostdtest.ShortDir("tw-ud-")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	env := append(os.Environ(), "TW_WORKERD="+wd, "TW_NO_UPDATE_CHECK=1", "TW_RELEASE_API="+api.URL)
	tw := func(args ...string) (string, time.Duration) {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, "tw"), append([]string{"--state-dir", state}, args...)...)
		cmd.Dir, cmd.Env = work, env
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		st := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("tw %s: %v\n%s\n%s", strings.Join(args, " "), err, so.String(), netd.Redact(se.String()))
		}
		return so.String(), time.Since(st)
	}
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("---- %s.log (tail)\n%s", c, netd.Redact(string(b[max(0, len(b)-6000):])))
			}
		}
		exec.Command(filepath.Join(bin, "tw"), "--state-dir", state, "down", "--purge").Run()
		os.RemoveAll(state)
	})
	keyFile := cliflow.WriteFakeOAuthFile(t, filepath.Join(work, "oauth.env"))
	up := []string{"up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--control-url", proxy.URL, "--tsapi-url", api.URL,
		"--hostname-prefix", "tw-ud-", "--mgmt-hostname", "tw-ud-mgmt", "--host-id", "tw-ud", "--json"}
	for i := range 3 {
		down := []string{"down", "--json"}
		if i == 2 {
			down = append(down, "--purge")
		}
		for _, c := range [][]string{up, down} {
			keys0 := kc.n.Load()
			so, took := tw(c...)
			var r struct {
				Timings map[string]float64 `json:"timingsMs"`
			}
			json.Unmarshal([]byte(so), &r)
			budget := upBudget
			if c[0] == "down" {
				budget = downBudget
			}
			t.Logf("tw %s (%d): %v, %d tsnet starts, phases (ms from start) %v", c[0], i+1, took.Round(time.Millisecond), kc.n.Load()-keys0, r.Timings)
			if took > budget {
				t.Errorf("tw %s (%d) took %v, budget %v", c[0], i+1, took.Round(time.Millisecond), budget)
			}
			if c[0] != "up" {
				continue
			}
			if n := kc.n.Load() - keys0; n > tsnetStartsPerUp {
				t.Errorf("tw up (%d) started %d tsnet nodes (GET /key), want %d: mgmt's node only", i+1, n, tsnetStartsPerUp)
			}
			// The single-use join key handoff is gone once mgmt has read it.
			if _, err := os.Stat(filepath.Join(state, "mgmt", "join.env")); !os.IsNotExist(err) {
				t.Errorf("tw up (%d) left mgmt's join key file (stat: %v)", i+1, err)
			}
		}
	}
}
