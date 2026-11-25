//go:build e2ereal

// Package regrate measures how fast one host registers fresh object nodes
// with production control (issue #114, RFC §13 Q1).
package regrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
)

const prodControl = "https://controlplane.tailscale.com"

// Only test tailnet 1. Never Ross's personal tailnet.
var allowedTailnets = map[string]bool{"tail7906d5.ts.net": true}

// TestRegistrationRateReal registers TW_REGRATE_N (default 50, at most 50)
// fresh ephemeral nodes, ttest-bench-<run>-*, from one netd registry with
// JoinParallel TW_REGRATE_PAR (default 16, netd's default) and reports the
// registrations per minute achieved and the per-node join and register
// latencies. By default one reusable key is minted up front so that key
// minting (an API call per object in production, unless a warm key is
// ready) is not part of the rate; TW_REGRATE_MINT=1 mints a single-use key
// per object inside the join instead. No policy is
// read or written; cleanup deletes exactly the devices and the key this run
// created.
func TestRegistrationRateReal(t *testing.T) {
	keyFile, tailnet := os.Getenv("TW_E2E_API_KEY_FILE"), os.Getenv("TW_E2E_TAILNET")
	if keyFile == "" || tailnet == "" {
		t.Skip("TW_E2E_API_KEY_FILE / TW_E2E_TAILNET not set")
	}
	if !allowedTailnets[tailnet] {
		t.Fatalf("refusing to run against tailnet %q (not in the allow list)", tailnet)
	}
	n, _ := strconv.Atoi(os.Getenv("TW_REGRATE_N"))
	if n <= 0 {
		n = 50
	}
	n = min(n, 50)
	par, _ := strconv.Atoi(os.Getenv("TW_REGRATE_PAR"))
	if par <= 0 {
		par = 16
	}
	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: apiKey}
	b := make([]byte, 3)
	rand.Read(b)
	prefix := "ttest-bench-" + hex.EncodeToString(b)
	tracker := tsapi.NewTracker(api)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	key, err := api.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{"tag:ttest"}, Ephemeral: true, Reusable: true,
		Preauthorized: true, ExpirySeconds: 900, Description: prefix})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "tw-regrate-")
	if err != nil {
		t.Fatal(err)
	}
	var keys netd.KeySource = netd.StaticKey(key.Secret)
	minter := tsapi.MintSource(api, nil)
	if os.Getenv("TW_REGRATE_MINT") != "" {
		keys = minter // one single-use key per object, minted in the join (production without warm keys)
	}
	reg, err := netd.New(netd.Config{ControlURL: prodControl, StateRoot: filepath.Join(dir, "nodes"),
		Keys: keys, Ephemeral: true, Tags: []string{"tag:ttest"}, JoinParallel: par,
		Hostname: func(id string) string { return prefix + "-" + id },
		Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, o := range reg.Objects() {
			tracker.Track(o.NodeID, o.ID)
		}
		reg.Close()
		os.RemoveAll(dir)
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		rep := tracker.Cleanup(cctx, prefix)
		kerr := api.DeleteKey(cctx, key.ID)
		minter.RevokeAll(cctx) // consumed single-use keys are already gone
		t.Logf("cleanup: tracked %d, deleted %d, left %d, untracked %v, key deleted: %v, errors %v",
			rep.Tracked, rep.Deleted, rep.Left, rep.Untracked, kerr == nil, rep.Errors)
		if err := rep.Err(); err != nil {
			t.Error(err)
		}
		if kerr != nil && !tsapi.IsNotFound(kerr) {
			t.Errorf("delete key: %v", kerr)
		}
	})

	st := time.Now()
	objs := make([]*netd.Object, n)
	for i := range n {
		if objs[i], err = reg.Create(fmt.Sprintf("%03d", i), netd.ObjectOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var readyAt []float64 // seconds from the first Create
	failed := 0
	for _, o := range objs {
		if err := o.Ready(ctx); err != nil {
			failed++
			t.Logf("%s: %v", o.ID(), err)
			continue
		}
	}
	total := time.Since(st)
	var joins, regs []float64
	slow := 0
	for _, o := range objs {
		in := o.Info()
		if in.State != netd.StateReady {
			continue
		}
		readyAt = append(readyAt, in.Spans.Listening/1000)
		joins = append(joins, in.JoinMS/1000)
		if in.Spans.Registered > 0 && in.Spans.RegisterSent > 0 {
			d := (in.Spans.Registered - in.Spans.RegisterSent) / 1000
			regs = append(regs, d)
			if d > 2 {
				slow++
			}
		}
	}
	slices.Sort(readyAt)
	ok := len(readyAt)
	t.Logf("N=%d par=%d: %d ready, %d failed in %.1f s: %.0f registrations/min overall", n, par, ok, failed, total.Seconds(), float64(ok)/total.Seconds()*60)
	if ok >= 10 {
		// Steady state: between the 10th and the last ready node (skips the
		// first wave's shared start-up).
		w := readyAt[ok-1] - readyAt[9]
		if w > 0 {
			t.Logf("steady: %.0f registrations/min (nodes 10..%d over %.1f s)", float64(ok-10)/w*60, ok, w)
		}
	}
	t.Logf("join (Create to ready) s: %s", quantiles(joins))
	t.Logf("register (RegisterRequest to response) s: %s; %d over 2 s", quantiles(regs), slow)
	if failed > 0 {
		t.Errorf("%d/%d nodes failed to join", failed, n)
	}
}

func quantiles(v []float64) string {
	if len(v) == 0 {
		return "n/a"
	}
	slices.Sort(v)
	q := func(p float64) float64 { return v[int(p*float64(len(v)-1))] }
	return fmt.Sprintf("p50 %.2f, p90 %.2f, max %.2f (n=%d)", q(0.5), q(0.9), v[len(v)-1], len(v))
}
