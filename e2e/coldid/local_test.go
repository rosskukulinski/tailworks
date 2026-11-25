//go:build e2e

package coldid

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// fakeKeys is the Tailscale key API for testcontrol (which accepts any key).
// Optional latency stands in for the production API's round trip.
func fakeKeys(t *testing.T, latency time.Duration) *httptest.Server {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth/token": // the OAuth client's exchange (mint mode oauth)
			w.Write([]byte(`{"access_token":"fake","token_type":"Bearer","expires_in":3600}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
			time.Sleep(latency)
			id := fmt.Sprintf("k%06d", n.Add(1))
			json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "testcontrol-key-" + id})
		default:
			w.WriteHeader(200)
			w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func TestColdIdentityLocal(t *testing.T) {
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	lat, _ := time.ParseDuration(os.Getenv("TW_COLDID_API_LATENCY"))
	api := fakeKeys(t, lat)
	kf := cliflow.WriteFakeOAuthFile(t, filepath.Join(t.TempDir(), "oauth.env"))
	e := env{name: "local", controlURL: ctl.URL, tailnet: "ttest.example", apiBase: api.URL, oauthFile: kf,
		tags: []string{"tag:ttest"}, prefix: "ttest-fi-local", n: envInt("TW_COLDID_N", 30),
		callerKey: func(context.Context) (string, error) { return nettest.DummyKey, nil },
		dial:      os.Getenv("TW_COLDID_DIAL")}
	if e.dial == "ready" {
		e.name += "-dialready" // the Phase 1 name
	} else if e.dial != "" && e.dial != "netmap" {
		e.name += "-dial" + e.dial
	}
	if os.Getenv("TW_COLDID_WARM") != "" {
		e.name += "-warm"
	}
	if os.Getenv("TW_COLDID_PREJOINED") != "" {
		e.prejoined = true
		e.name += "-prejoined"
	}
	ss := runColdIdentity(t, e)
	if len(ss) < e.n*9/10 {
		t.Fatalf("only %d/%d samples", len(ss), e.n)
	}
	report(t, e, ss)
}
