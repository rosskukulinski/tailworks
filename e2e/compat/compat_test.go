//go:build e2ecompat

// E2E-COMPAT on tailnet 1: Cloudflare's own example projects (projects.go),
// unmodified, deployed with `tw deploy` to a real tw-hostd host joined to a
// real tailnet (`tw up --oauth-client-file`, production control), and
// called from a tagged tsnet client node the way their READMEs call them on
// Cloudflare. Known gaps are expected to fail in the documented way; an
// unexpected pass fails the run so COMPAT.md gets updated.
//
// The API key in TW_E2E_API_KEY_FILE is used ONLY to (a) create and delete
// the OAuth client, (b) mint the test's own client node key, (c) apply the
// policy additions (validated, If-Match; the run REFUSES to write anything
// beyond the tag:tw-wl-compat tagOwner and its grant) and (d) clean up by
// ID.
package compat

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/internal/compatdoc"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const prodControl = "https://controlplane.tailscale.com"

// CompatTag tags every compat workload (writeOverlay).
const CompatTag = "tag:tw-wl-compat"

var allowedTailnets = map[string]bool{"tail7906d5.ts.net": true}

// Policy is the OAuth scenario's policy plus the compat tag and its grant.
// The grant includes tcp:443 for the TailnetHTTPS projects (#360): without
// it the client's connections to a workload's :443 listener are dropped
// and the probe times out (#614), as `tw deploy` prints for any app with
// tailnet.https (mgmt.CallerPorts).
func Policy() policy.Snippet {
	s := cliflow.Policy()
	s.TagOwners[CompatTag] = []string{"autogroup:admin", "tag:tw"}
	s.Grants = append(s.Grants, map[string]any{"src": []string{cliflow.TestTag}, "dst": []string{CompatTag}, "ip": []string{"tcp:80", "tcp:443"}})
	// A Worker calls its Companions over service bindings (a tailnet call).
	s.Grants = append(s.Grants, map[string]any{"src": []string{CompatTag}, "dst": []string{CompatTag}, "ip": []string{"tcp:80"}})
	return s
}

// approvedOnly: the run may add only the compat tag's owner and grant.
func approvedOnly(m policy.Snippet) error {
	for k := range m.TagOwners {
		if k != CompatTag {
			return fmt.Errorf("policy is also missing tagOwner %s (not approved for this run)", k)
		}
	}
	for _, g := range m.Grants {
		b, _ := json.Marshal(g)
		if !strings.Contains(string(b), `"`+CompatTag+`"`) {
			return fmt.Errorf("policy is also missing grant %s (not approved for this run)", b)
		}
	}
	if len(m.NodeAttrs) > 0 {
		return fmt.Errorf("policy is also missing %d nodeAttrs (not approved for this run)", len(m.NodeAttrs))
	}
	return nil
}

// Result is one project's outcome (the results file).
type Result struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	Exercises   []string `json:"exercises"`
	Outcome     string   `json:"outcome"` // PASS, FAIL, XFAIL, XPASS
	Detail      string   `json:"detail,omitempty"`
	PrepareMS   int64    `json:"prepareMs,omitempty"`
	DeployMS    int64    `json:"deployMs,omitempty"`
	ProbeMS     int64    `json:"probeMs,omitempty"`
	Steps       []string `json:"steps,omitempty"`
	Warnings    []string `json:"deployWarnings,omitempty"`
	Workarounds []string `json:"workarounds,omitempty"`
	Changes     []string `json:"changes,omitempty"`
}

func compatAnchors(t *testing.T, root string) map[string]bool {
	md, err := os.ReadFile(filepath.Join(root, "docs", "COMPAT.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows, errs := compatdoc.ParseCompat(md)
	if len(errs) > 0 {
		t.Fatalf("COMPAT.md: %v", errs)
	}
	a := map[string]bool{}
	for _, r := range rows {
		a[r.ID] = true
	}
	return a
}

func TestE2ECompat(t *testing.T) {
	root, _ := nettest.FindRepoRoot()
	if err := Validate(Projects(), compatAnchors(t, root)); err != nil {
		t.Fatal(err)
	}
	keyFile, tailnet := os.Getenv("TW_E2E_API_KEY_FILE"), os.Getenv("TW_E2E_TAILNET")
	if keyFile == "" || tailnet == "" {
		t.Skip("TW_E2E_API_KEY_FILE / TW_E2E_TAILNET not set")
	}
	if !allowedTailnets[tailnet] {
		t.Fatalf("refusing to run against tailnet %q (not in the allow list)", tailnet)
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TW_WORKERD", wd)
	par := 3
	if n, err := strconv.Atoi(os.Getenv("TW_COMPAT_PAR")); err == nil && n > 0 {
		par = n
	}

	// 0. Prepare every project before touching the tailnet: fetch the
	// pinned commits, npm install, build (cached).
	ps := selected(t)
	t0 := time.Now()
	prs := prepareAll(ps, CompatTag, 4)
	t.Logf("prepared %d projects in %v (cache %s)", len(ps), time.Since(t0).Round(time.Second), CacheDir())

	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: apiKey}
	b := make([]byte, 3)
	rand.Read(b)
	run := hex.EncodeToString(b)
	testPrefix, twPrefix, label := cliflow.RunPrefixes("compat", run)
	res := cliflow.NewResults(label+"-"+run, tailnet)
	stamp := time.Now().Format("20060102-150405")
	resultsTo := filepath.Join(root, "e2e", "real", "results", "compat-"+stamp+"-"+run+".json")
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	// 1. Policy: only the approved additions may be written (API key use (c)).
	cur, _, err := api.GetPolicyHuJSON(ctx)
	if err != nil {
		t.Fatalf("read policy: %v", netd.RedactError(err))
	}
	miss, err := policy.Missing(cur, Policy())
	if err != nil {
		t.Fatal(err)
	}
	if err := approvedOnly(miss); err != nil {
		t.Fatalf("refusing to write the policy: %v", err)
	}
	ar, err := policy.ApplyWith(ctx, api, Policy(), policy.Options{AllowTags: []string{cliflow.TestTag}, AllowSrcs: []string{"autogroup:admin"}})
	if err != nil {
		t.Fatalf("policy: %v", netd.RedactError(err))
	}
	if ar.Changed {
		bk := filepath.Join(os.Getenv("HOME"), ".config", "tailnet-agents-design", "tailnet1_acl.pre-compat-"+run+".hujson")
		os.WriteFile(bk, ar.Before, 0o600)
		res.Notes = append(res.Notes, "policy changed (validated, If-Match "+ar.OldETag+"): added "+ar.Added.HuJSON()+" (previous copy: "+bk+")")
	} else {
		res.Notes = append(res.Notes, "policy already had the compat snippet (not written)")
	}

	// 2. The OAuth client (API key use (a)).
	work := t.TempDir()
	oc, err := api.CreateOAuthClient(ctx, []string{"auth_keys", "devices:core", "policy_file:read"}, []string{"tag:tw"}, testPrefix+"oauth")
	if err != nil {
		t.Fatalf("create oauth client: %v", netd.RedactError(err))
	}
	clientFile := filepath.Join(work, "oauth.env")
	if err := os.WriteFile(clientFile, []byte("TS_OAUTH_CLIENT_ID="+oc.ID+"\nTS_OAUTH_CLIENT_SECRET="+oc.Secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oc.Secret = ""

	quiet := slog.New(netd.NewRedactingHandler(slog.NewTextHandler(io.Discard, nil)))
	minter := tsapi.MintSource(api, quiet)
	tracker := tsapi.NewTracker(api)
	bin := cliflow.BuildBinaries(t)
	state := filepath.Join("/tmp", "tw-e2e-compat-"+run)
	var client *nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: tailnet, OAuthClientFile: clientFile,
		ControlURL: prodControl, Prefix: twPrefix, Res: res, ClientTagged: true}
	tw := func(dir, stdin string, args ...string) (string, string, time.Duration, error) {
		cmd := exec.Command(filepath.Join(bin, "tw"), append([]string{"--state-dir", state}, args...)...)
		cmd.Dir = cmp.Or(dir, work)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr, cmd.Stdin = &so, &se, strings.NewReader(stdin)
		st := time.Now()
		err := cmd.Run()
		return so.String(), netd.Redact(se.String()), time.Since(st), err
	}
	var (
		resultsMu sync.Mutex
		results   []Result
	)
	downDone := false
	t.Cleanup(func() {
		if !downDone {
			e.TrackObjectNodes()
			if _, se, _, err := e.Tw("down", "--purge"); err != nil {
				t.Logf("tw down (cleanup): %v %s", err, netd.Redact(se))
			}
		}
		for _, c := range []string{"supervisor", "mgmt", "hostd"} {
			lb, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
			if t.Failed() {
				t.Logf("%s log (tail):\n%s", c, netd.Redact(string(lb[max(0, len(lb)-8000):])))
			}
		}
		if client != nil {
			client.Close()
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer ccancel()
		devs, keys := cliflow.Leftovers(cctx, api, res)
		for _, id := range res.NodeIDs {
			tracker.Track(id, "tw")
		}
		for _, id := range res.InfraNodeIDs {
			tracker.Track(id, "tw-infra")
		}
		time.Sleep(3 * time.Second)
		crep := tracker.Cleanup(cctx, testPrefix)
		var untrackedTw []string
		if all, err := api.ListDevices(cctx); err == nil {
			for _, d := range all {
				if strings.HasPrefix(d.Hostname, twPrefix) || strings.HasPrefix(d.Name, twPrefix) || strings.HasPrefix(d.Hostname, "tw-host-"+strings.TrimSuffix(twPrefix, "-")) {
					untrackedTw = append(untrackedTw, d.Name)
				}
			}
		}
		for _, k := range keys {
			api.DeleteKey(cctx, k)
		}
		var errs []string
		if err := api.DeleteKey(cctx, oc.ID); err != nil {
			errs = append(errs, "oauth client "+oc.ID+": "+netd.Redact(err.Error()))
		}
		if err := minter.RevokeAll(cctx); err != nil {
			errs = append(errs, netd.Redact(err.Error()))
		}
		for _, id := range res.KeyIDs {
			api.DeleteKey(cctx, id)
		}
		res.Cleanup["devices_left"] = crep.Left
		res.Cleanup["untracked_run_devices"] = crep.Untracked
		res.Cleanup["tw_named_devices_left"] = untrackedTw
		res.Cleanup["errors"] = errs
		os.RemoveAll(state)
		writeRunFile(t, resultsTo, "deployed", root, res, results)
		if len(devs) > 0 || len(keys) > 0 {
			t.Errorf("after tw down: leftover tw devices %v, valid keys %d", devs, len(keys))
		}
		if crep.Left != 0 || len(crep.Untracked) > 0 || len(untrackedTw) > 0 || len(errs) > 0 {
			t.Errorf("cleanup: %+v tw-named left %v errors %v", crep, untrackedTw, errs)
		}
	})

	// 3. tw up (OAuth) with a separate tw-hostd host on its own node.
	var up struct {
		TenantID   string `json:"tenantID"`
		MgmtNodeID string `json:"mgmtNodeID"`
	}
	d := e.MustJSON(&up, e.UpCommand()...)
	e.TrackInfra(up.MgmtNodeID)
	res.Numbers["up_ready_ms"] = float64(d.Milliseconds())
	res.Pass("up", "tenant "+up.TenantID)

	// 4. The client: a tag:ttest tsnet node (API key use (b)).
	k, err := minter.AuthKey(ctx, netd.KeyRequest{ObjectID: "client", Tags: []string{cliflow.TestTag}, Ephemeral: true})
	if err != nil {
		t.Fatalf("mint client: %v", netd.RedactError(err))
	}
	client, err = nettest.NewNode(ctx, prodControl, k, testPrefix+"client", filepath.Join(work, "client"))
	if err != nil {
		t.Fatalf("client: %v", netd.RedactError(err))
	}
	if st, err := client.LC.StatusWithoutPeers(ctx); err == nil && st.Self != nil {
		tracker.Track(string(st.Self.ID), "client")
	}

	// 5. Every project, par at a time.
	sem := make(chan struct{}, par)
	t.Run("projects", func(t *testing.T) {
		for _, p := range ps {
			pr := prs[p.ID]
			t.Run(p.ID, func(t *testing.T) {
				t.Parallel()
				sem <- struct{}{}
				defer func() { <-sem }()
				r := runProject(t, pr, tw, client)
				resultsMu.Lock()
				results = append(results, r)
				resultsMu.Unlock()
				switch r.Outcome {
				case "PASS", "XFAIL":
					t.Logf("%s: %s", r.Outcome, r.Detail)
				case "XPASS":
					t.Errorf("XPASS: %s is expected to fail (COMPAT.md #%s) but passed: update COMPAT.md and the project", p.ID, p.ExpectIn("deployed").Compat)
				default:
					t.Errorf("FAIL: %s", r.Detail)
				}
			})
		}
	})
	e.TrackObjectNodes()

	// 6. Down.
	if _, se, _, err := e.Tw("down", "--purge"); err != nil {
		t.Fatalf("tw down: %v %s", err, netd.Redact(se))
	}
	downDone = true
	res.Pass("down", "purged")
}

// twFunc runs tw in dir ("" = the run's work dir) with stdin.
type twFunc func(dir, stdin string, args ...string) (string, string, time.Duration, error)

func runProject(t *testing.T, pr *Prepared, tw twFunc, client *nettest.Node) Result {
	p := pr.P
	r := Result{ID: p.ID, Source: fmt.Sprintf("%s/%s@%s", p.Repo, p.Dir, p.Commit[:12]), Exercises: p.Exercises,
		PrepareMS: pr.Dur.Milliseconds(), Workarounds: p.Workarounds, Changes: p.Changes}
	fail := func(f string, a ...any) Result {
		r.Outcome, r.Detail = "FAIL", fmt.Sprintf(f, a...)
		return r
	}
	if pr.Err != nil {
		return fail("prepare: %v", pr.Err)
	}
	if err := deployCompanions(p, tw); err != nil { // companions_test.go
		return fail("%v", err)
	}
	for name, v := range p.Secrets {
		if _, se, _, err := tw(pr.DeployDir, v, "secret", "put", name, "--workload", p.ID); err != nil {
			return fail("tw secret put %s: %v %s", name, err, se)
		}
	}
	ex := p.ExpectIn("deployed")
	refused := func(so, se string) Result {
		msg := errorLines(se + "\n" + so)
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(ex.Error)) {
			return fail("tw deploy refused it, but its error doesn't mention %q: %s", ex.Error, tail(se+so, 1500))
		}
		r.Outcome, r.Detail = "XFAIL", "refused as expected: "+tail(msg, 400)
		return r
	}
	if p.OriginVar != "" {
		if err := resetOriginVar(p, pr.Root); err != nil {
			return fail("%v", err)
		}
	}
	args := append([]string{"deploy", pr.DeployDir, "--json", "--wait", "4m"}, p.DeployArgs...)
	so, se, dur, err := tw("", "", args...)
	r.DeployMS = dur.Milliseconds()
	for _, l := range strings.Split(se, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "›") || strings.Contains(l, "warning") || strings.Contains(l, "ignored") {
			r.Warnings = append(r.Warnings, strings.TrimSpace(l))
		}
	}
	// With an OriginVar the deploy that counts is the second one, below.
	if ex.Kind == ExpectRefused && p.OriginVar == "" {
		if err == nil {
			r.Outcome, r.Detail = "XPASS", "tw deploy accepted it"
			return r
		}
		return refused(so, se)
	}
	if err != nil {
		return fail("tw deploy: %v\n%s\n%s", err, tail(se, 3000), tail(so, 1000))
	}
	var dep struct {
		Endpoint struct {
			MagicDNS string `json:"magicDNS"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(so), &dep); err != nil || dep.Endpoint.MagicDNS == "" {
		return fail("tw deploy --json: %v %s", err, tail(so, 500))
	}
	for _, s := range p.Setup {
		a := append(append([]string{}, s...), "--workload", p.ID)
		if _, se, _, err := tw(pr.DeployDir, "", a...); err != nil {
			return fail("tw %s: %v %s", strings.Join(a, " "), err, se)
		}
	}
	ip := cliflow.PeerIP(t, client, dep.Endpoint.MagicDNS, 90*time.Second)
	if p.OriginVar != "" {
		// The README's "set it to your deployed URL and deploy again".
		if err := setOriginVar(p, pr.Root, p.OriginVar, deployedBase(p, ip, dep.Endpoint.MagicDNS)+"/mcp"); err != nil { // tailnethttps.go
			return fail("%v", err)
		}
		so, se, _, err := tw("", "", args...)
		switch {
		case ex.Kind == ExpectRefused && err == nil:
			r.Outcome, r.Detail = "XPASS", "tw deploy accepted it with "+p.OriginVar+" set"
			return r
		case ex.Kind == ExpectRefused:
			return refused(so, se)
		case err != nil:
			return fail("tw deploy (with %s set): %v\n%s", p.OriginVar, err, tail(se, 3000))
		}
	}
	var certStep []string
	if p.TailnetHTTPS {
		d, err := waitTailnetCert(context.Background(), client.Dial, ip, dep.Endpoint.MagicDNS) // tailnethttps.go
		if err != nil {
			return fail("%v", err)
		}
		certStep = []string{fmt.Sprintf("https: certificate ready after %s", d.Round(time.Second))}
	}
	run := &Run{P: p, Base: deployedBase(p, ip, dep.Endpoint.MagicDNS), HC: deployedClient(client.Dial, ip, 60*time.Second),
		Tw: func(args ...string) (string, string, error) {
			so, se, _, err := tw("", "", args...)
			return so, se, err
		}, Steps: certStep}
	t0 := time.Now()
	perr := p.Probe(run)
	r.ProbeMS, r.Steps = time.Since(t0).Milliseconds(), run.Steps
	switch {
	case ex.Kind == ExpectProbeFail && perr == nil:
		r.Outcome, r.Detail = "XPASS", "probe passed"
	case ex.Kind == ExpectProbeFail:
		r.Outcome, r.Detail = "XFAIL", "failed as expected ("+ex.Why+"): "+tail(perr.Error(), 300)
	case perr != nil:
		if lo, _, _, err := tw("", "", "logs", p.ID, "--console", "-n", "40"); err == nil {
			r.Steps = append(r.Steps, "console (tail): "+tail(lo, 2000))
		}
		if lo, _, _, err := tw("", "", "logs", p.ID, "-n", "40"); err == nil {
			r.Steps = append(r.Steps, "request records (tail): "+tail(lo, 4000))
		}
		return fail("probe: %v", perr)
	default:
		r.Outcome, r.Detail = "PASS", fmt.Sprintf("%d steps", len(run.Steps))
	}
	return r
}

// errorLines is the error output of a failed tw command: the "error …"
// line and what follows it, or (for --json) the "error" event's message —
// never the start events, whose paths can contain anything.
func errorLines(out string) string {
	var keep []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "{") {
			var ev struct {
				Error *struct {
					Code, Message string
					Detail        []string
				} `json:"error"`
			}
			if json.Unmarshal([]byte(t), &ev) == nil && ev.Error != nil {
				keep = append(keep, strings.TrimSpace(ev.Error.Code+": "+ev.Error.Message+" "+strings.Join(ev.Error.Detail, " ")))
			}
			continue
		}
		if strings.HasPrefix(t, "error") {
			in = true
		}
		if in && t != "" {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, " ")
}

// writeRunFile writes the JSON results (the RunFile fields, plus the
// cliflow results of a deployed run) and logs the matrix.
func writeRunFile(t *testing.T, path, mode, root string, res *cliflow.Results, results []Result) {
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	counts := map[string]int{}
	var tbl strings.Builder
	fmt.Fprintf(&tbl, "| project | outcome | deploy | probe | detail |\n|---|---|---|---|---|\n")
	for _, r := range results {
		counts[r.Outcome]++
		fmt.Fprintf(&tbl, "| %s | %s | %.1fs | %.1fs | %s |\n", r.ID, r.Outcome, float64(r.DeployMS)/1000, float64(r.ProbeMS)/1000,
			strings.ReplaceAll(tail(strings.ReplaceAll(r.Detail, "\n", " "), 160), "|", "\\|"))
	}
	commit, _ := run(root, nil, "git", "rev-parse", "HEAD")
	out := struct {
		*cliflow.Results
		Mode     string         `json:"mode"`
		Date     string         `json:"date"`
		Commit   string         `json:"commit"`
		Platform string         `json:"platform"`
		Projects []Result       `json:"projects"`
		Counts   map[string]int `json:"counts"`
	}{res, mode, time.Now().UTC().Format("2006-01-02"), strings.TrimSpace(commit), runtime.GOOS + "/" + runtime.GOARCH, results, counts}
	b, _ := json.MarshalIndent(out, "", "  ")
	b = []byte(netd.Redact(string(b)))
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, b, 0o644)
	t.Logf("results: %s\ncounts: %v\n%s", path, counts, tbl.String())
}
