//go:build e2emac

// E2E-MAC (the macOS end-to-end acceptance test) on this machine against
// tailnet 1, driven only through the tailworks CLI: tw up (apikey mode, temp
// --state-dir) → deploy the agent and mcp templates → a tag:ttest tsnet
// client + a private tag:ttest echo service check (a)–(f) → tw down →
// zero leftover devices and keys from this run.
//
// Safety: only tailnet 1 (allow list). The policy additions the scenario
// needs (cliflow.Policy) are applied validated + If-Match, keeping every
// existing rule; cleanup deletes exactly the IDs this run created. Keys are
// never printed.
package mac

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const prodControl = "https://controlplane.tailscale.com"

var allowedTailnets = map[string]bool{"tail7906d5.ts.net": true}

func TestE2EMac(t *testing.T) {
	keyFile, tailnet := os.Getenv("TW_E2E_API_KEY_FILE"), os.Getenv("TW_E2E_TAILNET")
	if keyFile == "" || tailnet == "" {
		t.Skip("TW_E2E_API_KEY_FILE / TW_E2E_TAILNET not set")
	}
	if !allowedTailnets[tailnet] {
		t.Fatalf("refusing to run against tailnet %q (not in the allow list)", tailnet)
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TW_WORKERD", wd)
	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: apiKey}
	// tailworks mints with an OAuth client: this run's own, created with the
	// harness's API key and deleted when the test ends.
	oauthFile := cliflow.RealOAuthClient(t, keyFile, tailnet, t.TempDir(), "tw-e2e-mac-oauth")
	b := make([]byte, 3)
	rand.Read(b)
	run := hex.EncodeToString(b)
	prefix := "tw-e2e-" + run + "-"
	if p := os.Getenv("TW_E2E_PREFIX"); p != "" { // e.g. ttest-rtf-: name test nodes per run
		prefix = p + run + "-"
	}
	root, _ := nettest.FindRepoRoot()
	res := cliflow.NewResults("m4-"+run, tailnet)
	resultsTo := filepath.Join(root, "e2e", "real", "results", "m4-"+time.Now().Format("20060102-150405")+"-"+run+".json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Policy: add what the scenario needs (no-op if present).
	ar, err := policy.ApplyWith(ctx, api, cliflow.Policy(), policy.Options{AllowTags: []string{cliflow.TestTag}})
	if err != nil {
		t.Fatalf("policy: %v", netd.RedactError(err))
	}
	if ar.Changed {
		bk := filepath.Join(os.Getenv("HOME"), ".config", "tailnet-agents-design", "tailnet1_acl.pre-m4-"+run+".hujson")
		os.WriteFile(bk, ar.Before, 0o600)
		res.Notes = append(res.Notes, "policy changed (validated, If-Match "+ar.OldETag+"): added "+ar.Added.HuJSON()+" (previous copy: "+bk+")")
	} else {
		res.Notes = append(res.Notes, "policy already had the scenario's tagOwners and grants (not written)")
	}

	quiet := slog.New(netd.NewRedactingHandler(slog.NewTextHandler(io.Discard, nil)))
	minter := tsapi.MintSource(api, quiet)
	tracker := tsapi.NewTracker(api)
	bin := cliflow.BuildBinaries(t)
	state := filepath.Join("/tmp", "tw-e2e-"+run)
	work := t.TempDir()
	var nodes []*nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: tailnet, OAuthClientFile: oauthFile,
		ControlURL: prodControl, Prefix: prefix, Res: res, ClientTagged: true, PolicyGrantsParentRefund: true,
		NewNode: func(role string) *nettest.Node {
			k, err := minter.AuthKey(ctx, netd.KeyRequest{ObjectID: role, Tags: []string{cliflow.TestTag}, Ephemeral: true})
			if err != nil {
				t.Fatalf("mint %s: %v", role, netd.RedactError(err))
			}
			n, err := nettest.NewNode(ctx, prodControl, k, prefix+role, filepath.Join(work, role))
			if err != nil {
				t.Fatalf("%s: %v", role, netd.RedactError(err))
			}
			nodes = append(nodes, n)
			if st, err := n.LC.StatusWithoutPeers(ctx); err == nil && st.Self != nil {
				tracker.Track(string(st.Self.ID), role)
			}
			return n
		}}
	downDone := false
	t.Cleanup(func() {
		if !downDone {
			if _, se, _, err := e.Tw("down", "--purge"); err != nil {
				t.Logf("tw down (cleanup): %v %s", err, netd.Redact(se))
			}
		}
		for _, c := range []string{"supervisor", "mgmt", "hostd"} {
			if t.Failed() {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, netd.Redact(string(b[max(0, len(b)-6000):])))
			}
		}
		for _, n := range nodes {
			n.Close()
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		devs, keys := cliflow.Leftovers(cctx, api, res)
		res.Cleanup["tw_devices_tracked"] = len(res.NodeIDs)
		res.Cleanup["tw_keys_tracked"] = len(res.KeyIDs)
		res.Cleanup["tw_devices_left_after_down"] = len(devs)
		res.Cleanup["tw_keys_valid_after_down"] = len(keys)
		// Safety net: delete exactly this run's leftovers, by ID.
		for _, id := range res.NodeIDs {
			tracker.Track(id, "tw")
		}
		for _, id := range res.InfraNodeIDs {
			tracker.Track(id, "tw-infra") // mgmt's and the host's own nodes: kept by tw down, deleted here by ID
		}
		crep := tracker.Cleanup(cctx, prefix)
		for _, k := range keys {
			api.DeleteKey(cctx, k)
		}
		var revErrs []string
		if err := minter.RevokeAll(cctx); err != nil {
			revErrs = append(revErrs, netd.Redact(err.Error()))
		}
		res.Cleanup["devices_tracked"] = crep.Tracked
		res.Cleanup["devices_deleted_by_cleanup"] = crep.Deleted
		res.Cleanup["devices_left"] = crep.Left
		res.Cleanup["untracked_run_devices"] = crep.Untracked
		res.Cleanup["test_keys_minted"] = len(minter.KeyIDs())
		res.Cleanup["test_keys_revoke_errors"] = revErrs
		os.RemoveAll(state)
		os.MkdirAll(filepath.Dir(resultsTo), 0o755)
		out, _ := json.MarshalIndent(res, "", "  ")
		out = []byte(netd.Redact(string(out)))
		os.WriteFile(resultsTo, out, 0o644)
		t.Logf("results: %s\n%s", resultsTo, out)
		if len(devs) > 0 || len(keys) > 0 {
			t.Errorf("after tw down: leftover tw devices %v, valid keys %d", devs, len(keys))
		}
		if crep.Left != 0 || len(crep.Untracked) > 0 {
			t.Errorf("cleanup left devices: %+v", crep)
		}
	})
	e.Run()
	e.Down()
	downDone = true
	// Device deletes propagate asynchronously; give the API a moment.
	time.Sleep(2 * time.Second)
}
