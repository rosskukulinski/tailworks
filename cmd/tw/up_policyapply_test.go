package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// fakePolicyAPI is the Tailscale API's policy file: GET /acl (with an
// ETag), POST /acl/validate and POST /acl (If-Match).
type fakePolicyAPI struct {
	mu       sync.Mutex
	policy   string
	version  int
	gets     int
	validate int
	posts    int // POST /acl attempts
	writes   int // POST /acl that changed the policy

	denyValidate bool // 403 on validate: a client with Policy File read only
	denyWrite    bool // 403 on the write
	// conflicts: this many POST /acl first lose to a concurrent edit (the
	// policy gains raceLine, and the write answers 412).
	conflicts int
}

const raceLine = "// edited by someone else\n"

func (a *fakePolicyAPI) etag() string { return `"v` + strconv.Itoa(a.version) + `"` }

func (a *fakePolicyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	denied := func() {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"calling actor does not have enough permissions to perform this function"}`))
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
		a.gets++
		w.Header().Set("ETag", a.etag())
		w.Write([]byte(a.policy))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/acl/validate"):
		a.validate++
		if a.denyValidate {
			denied()
			return
		}
		w.Write([]byte(`{}`))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/acl"):
		a.posts++
		if a.denyWrite {
			denied()
			return
		}
		if a.conflicts > 0 {
			a.conflicts--
			a.policy = raceLine + a.policy
			a.version++
		}
		if r.Header.Get("If-Match") != a.etag() {
			w.WriteHeader(412)
			w.Write([]byte(`{"message":"precondition failed, invalid old hash"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		a.policy = string(body)
		a.version++
		a.writes++
		w.Header().Set("ETag", a.etag())
		w.Write(body)
	default:
		w.WriteHeader(404)
	}
}

// upApplyEnv runs upApplyPolicy as tw up does: the policy is read first
// (oauthPreflight), then handed over.
type upApplyEnv struct {
	api *fakePolicyAPI
	c   *tsapi.Client
}

func newUpApplyEnv(t *testing.T, pol string) *upApplyEnv {
	t.Helper()
	a := &fakePolicyAPI{policy: pol}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return &upApplyEnv{api: a, c: &tsapi.Client{Tailnet: "ttest.ts.net", APIKey: "fake", BaseURL: srv.URL}}
}

// run is tw up --apply-policy [--yes] [--json] on a single-process layout.
// terminal and input are the prompt's.
func (e *upApplyEnv) run(t *testing.T, terminal bool, input string, args ...string) (pol string, applied bool, out string, err error) {
	t.Helper()
	oldT, oldIn := confirmIsTerminal, confirmIn
	t.Cleanup(func() { confirmIsTerminal, confirmIn = oldT, oldIn })
	confirmIsTerminal = func() bool { return terminal }
	confirmIn = strings.NewReader(input)
	f := newFlags("up")
	yes := f.Bool("yes", false, "")
	if _, err := f.parse(args); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jsonOut = false })
	ctx := context.Background()
	before, _, gerr := e.c.GetPolicyHuJSON(ctx)
	if gerr != nil {
		t.Fatal(gerr)
	}
	needed := policy.Snippet{TagOwners: map[string][]string{mgmt.MgmtTag: {"tag:tw"}}}
	var b bytes.Buffer
	after, applied, err := upApplyPolicy(ctx, e.c, f, before, true, needed, policyApplyFlag, *yes, &b)
	return string(after), applied, b.String(), err
}

func (e *upApplyEnv) now() (pol string, writes, posts int) {
	e.api.mu.Lock()
	defer e.api.mu.Unlock()
	return e.api.policy, e.api.writes, e.api.posts
}

// freshPolicy is a tailnet after quick-start step 2's one line: tag:tw
// exists (the OAuth client is tagged with it), nothing else of tailworks.
const freshPolicy = `// ours
{
  "tagOwners": {
    "tag:tw": ["autogroup:admin"],
    "tag:db": ["autogroup:admin"],
  },
  "grants": [
    {"src": ["group:eng"], "dst": ["tag:db"], "ip": ["tcp:5432"]}, // keep me
  ],
}`

// bootstrapMissing is what freshPolicy lacks of operatorSnippet.
func bootstrapMissing() policy.Snippet {
	s := operatorSnippet()
	delete(s.TagOwners, "tag:tw")
	return s
}

// operatorMissing fails unless pol has all of operatorSnippet.
func operatorMissing(t *testing.T, pol string) {
	t.Helper()
	miss, err := policy.Missing([]byte(pol), operatorSnippet())
	if err != nil || !miss.Empty() {
		t.Fatalf("the policy still lacks %s (%v):\n%s", miss.HuJSON(), err, pol)
	}
}

// TestUpApplyPolicyApplied (issue #320): tw up --apply-policy --yes shows
// the lines and writes exactly the bootstrap tagOwners and grants.
func TestUpApplyPolicyApplied(t *testing.T) {
	e := newUpApplyEnv(t, freshPolicy)
	after, applied, out, err := e.run(t, false, "", "--yes")
	if err != nil || !applied {
		t.Fatalf("--apply-policy --yes: applied=%v err=%v\n%s", applied, err, out)
	}
	for _, want := range []string{`+ "tagOwners": {`, `+   "tag:tw-mgmt": ["autogroup:admin", "tag:tw"],`, `+   "tag:tw-host": ["autogroup:admin", "tag:tw"],`,
		`"dst": ["tag:tw-host"]`, mgmt.AdminCap, "✓ policy   applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"tag:tw":`) {
		t.Errorf("the diff has tag:tw, which the policy already defines:\n%s", out)
	}
	now, writes, posts := e.now()
	if writes != 1 || posts != 1 || now != after {
		t.Fatalf("%d writes in %d POSTs; returned policy matches the server's: %v", writes, posts, now == after)
	}
	if !strings.Contains(now, "// keep me") || !strings.Contains(now, "// ours") {
		t.Fatalf("comments lost:\n%s", now)
	}
	if err := policy.VerifyAdditive([]byte(freshPolicy), []byte(now), bootstrapMissing()); err != nil {
		t.Fatal(err)
	}
	operatorMissing(t, now)

	// Again: already present, so nothing is validated or written.
	e.api.mu.Lock()
	validated := e.api.validate
	e.api.mu.Unlock()
	after, applied, out, err = e.run(t, false, "") // no --yes needed: no write
	if err != nil || applied || after != now || !strings.Contains(out, "nothing to write") {
		t.Fatalf("second --apply-policy: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, writes, posts := e.now(); writes != 1 || posts != 1 || e.api.validate != validated {
		t.Fatalf("already present, but %d writes, %d POSTs, %d validations", writes, posts, e.api.validate-validated)
	}
}

// An existing tag keeps the owners its admin chose: tag:tw is left alone
// and tag:tw-mgmt only gains tag:tw (never autogroup:admin).
func TestUpApplyPolicyDoesNotWidenOwners(t *testing.T) {
	const pol = `{
  "tagOwners": {
    "tag:tw": ["group:platform"],
    "tag:tw-mgmt": ["group:platform"],
  },
}`
	e := newUpApplyEnv(t, pol)
	after, applied, out, err := e.run(t, false, "", "--yes")
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v\n%s", applied, err, out)
	}
	want := operatorSnippet()
	delete(want.TagOwners, "tag:tw")
	want.TagOwners[mgmt.MgmtTag] = []string{"tag:tw"}
	if err := policy.VerifyAdditive([]byte(pol), []byte(after), want); err != nil {
		t.Fatalf("%v\n%s", err, after)
	}
}

// No Policy File write scope: nothing is written, tw up says why, and goes
// on as it does without the flag.
func TestUpApplyPolicyNoWriteScope(t *testing.T) {
	// tag:tw doesn't own tag:tw-mgmt: tw up can't start, as without the
	// flag, and the error carries the snippet to paste.
	for _, deny := range []string{"validate", "write"} {
		e := newUpApplyEnv(t, freshPolicy)
		e.api.denyValidate, e.api.denyWrite = deny == "validate", true
		after, applied, out, err := e.run(t, false, "", "--yes")
		if cliCode(err) != "policy_missing_tag_owners" || applied || after != freshPolicy {
			t.Fatalf("403 on %s: applied=%v err=%v\n%s", deny, applied, err, out)
		}
		if !strings.Contains(out, "not applied: the OAuth client lacks the Policy File › Write scope") {
			t.Errorf("403 on %s: no note on why it wasn't applied:\n%s", deny, out)
		}
		if txt := cliText(err); !strings.Contains(txt, `"tag:tw-mgmt": ["autogroup:admin", "tag:tw"]`) || !strings.Contains(txt, "no Policy File › Write scope") {
			t.Errorf("403 on %s: the error lacks the snippet or the reason:\n%s", deny, txt)
		}
		if now, writes, _ := e.now(); writes != 0 || now != freshPolicy {
			t.Fatalf("403 on %s: the policy was written", deny)
		}
	}

	// The tagOwners are there and only grants are missing: tw up goes on
	// (and prints the missing grants at the end, as without the flag).
	withOwners, err := policy.Merge([]byte(freshPolicy), tagOwnersPart())
	if err != nil {
		t.Fatal(err)
	}
	e := newUpApplyEnv(t, string(withOwners))
	e.api.denyValidate, e.api.denyWrite = true, true
	after, applied, out, err := e.run(t, false, "") // no --yes: it never gets to the prompt
	if err != nil || applied || after != string(withOwners) || !strings.Contains(out, "not applied: the OAuth client lacks") {
		t.Fatalf("grants only, no write scope: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, writes, posts := e.now(); writes != 0 || posts != 0 {
		t.Fatalf("no write scope, but %d POSTs", posts)
	}

	// The client can't read the policy at all: no API call, the whole
	// snippet is printed.
	var b bytes.Buffer
	f := newFlags("up")
	pol, applied, err := upApplyPolicy(context.Background(), e.c, f, nil, false, policy.Snippet{}, policyApplyFlag, true, &b)
	if err != nil || applied || pol != nil {
		t.Fatalf("unreadable policy: applied=%v err=%v", applied, err)
	}
	for _, want := range []string{"not applied", "Policy File › Write", `"tag:tw-host": ["autogroup:admin", "tag:tw"]`, mgmt.AdminCap} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("unreadable policy: output lacks %q:\n%s", want, b.String())
		}
	}
}

// The policy changes between the read and the If-Match write: read, merge
// and write once more, keeping the other edit. Twice in a row: stop.
func TestUpApplyPolicyConflict(t *testing.T) {
	e := newUpApplyEnv(t, freshPolicy)
	e.api.conflicts = 1
	after, applied, out, err := e.run(t, false, "", "--yes")
	if err != nil || !applied {
		t.Fatalf("one conflict: applied=%v err=%v\n%s", applied, err, out)
	}
	now, writes, posts := e.now()
	if writes != 1 || posts != 2 {
		t.Fatalf("one conflict: %d writes in %d POSTs, want 1 in 2", writes, posts)
	}
	if !strings.HasPrefix(now, raceLine) || now != after {
		t.Fatalf("the concurrent edit was lost, or the returned policy is stale:\n%s", now)
	}
	operatorMissing(t, now)

	e = newUpApplyEnv(t, freshPolicy)
	e.api.conflicts = 2
	_, applied, out, err = e.run(t, false, "", "--yes")
	if cliCode(err) != "policy_conflict" || applied {
		t.Fatalf("two conflicts: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, writes, posts := e.now(); writes != 0 || posts != 2 {
		t.Fatalf("two conflicts: %d writes in %d POSTs, want 0 in 2 (one retry)", writes, posts)
	}
}

// The prompt: y writes, anything else doesn't; without a terminal (or with
// --json) --yes is required.
func TestUpApplyPolicyConfirm(t *testing.T) {
	e := newUpApplyEnv(t, freshPolicy)
	after, applied, out, err := e.run(t, true, "n\n")
	if cliCode(err) != "cancelled" || applied || after != freshPolicy {
		t.Fatalf("declined: applied=%v err=%v\n%s", applied, err, out)
	}
	if !strings.Contains(out, `+   "tag:tw-mgmt"`) {
		t.Errorf("declined before the lines were shown:\n%s", out)
	}
	for name, run := range map[string]func() (string, bool, string, error){
		"no terminal":          func() (string, bool, string, error) { return e.run(t, false, "y\n") },
		"--json on a terminal": func() (string, bool, string, error) { return e.run(t, true, "y\n", "--json") },
	} {
		_, applied, out, err := run()
		if cliCode(err) != "confirmation_required" || applied {
			t.Fatalf("%s: applied=%v err=%v\n%s", name, applied, err, out)
		}
		if txt := cliText(err); !strings.Contains(txt, "tw up --apply-policy --yes") || !strings.Contains(txt, `"tag:tw-mgmt"`) {
			t.Errorf("%s: the refusal lacks --yes or the lines to paste:\n%s", name, txt)
		}
		if !strings.Contains(out, `+   "tag:tw-mgmt"`) {
			t.Errorf("%s: the lines were not shown:\n%s", name, out)
		}
	}
	if now, writes, posts := e.now(); writes != 0 || posts != 0 || now != freshPolicy {
		t.Fatalf("declined or unconfirmed, but %d POSTs", posts)
	}

	after, applied, out, err = e.run(t, true, "y\n")
	if err != nil || !applied {
		t.Fatalf("answered y: applied=%v err=%v\n%s", applied, err, out)
	}
	operatorMissing(t, after)
}

// --apply-policy is the only spelling, on tw up and tw tenant add: the
// earlier --policy-apply is refused, not kept as an alias.
func TestApplyPolicyFlagSpelling(t *testing.T) {
	if fs := upFlagSet(t); fs.Lookup("apply-policy") == nil || fs.Lookup("policy-apply") != nil || fs.Lookup("yes") == nil {
		t.Error("tw up: want --apply-policy and --yes, and no --policy-apply")
	}
	for _, run := range []func([]string) error{cmdUp, cmdTenantAdd} {
		if err := run([]string{"--policy-apply", "--state-dir", t.TempDir() + "/never-created"}); cliCode(err) != "bad_request" {
			t.Errorf("--policy-apply: %v, want bad_request", err)
		}
	}
	var h *helpRequest
	if err := cmdTenantAdd([]string{"--help"}); !errors.As(err, &h) {
		t.Fatalf("tw tenant add --help: %v", err)
	}
	if h.f.Lookup("apply-policy") == nil || h.f.Lookup("yes") == nil {
		t.Error("tw tenant add: want --apply-policy and --yes")
	}
}

// Headscale asks too (it wrote without asking before #320): the snippet
// is shown, and without a terminal --yes is required.
func TestHeadscaleUpPolicyConfirm(t *testing.T) {
	const pol = `{"tagOwners": {}}`
	e := newUpApplyEnv(t, pol)
	run := func(terminal bool, input string, yes bool) (string, bool, string, error) {
		oldT, oldIn := confirmIsTerminal, confirmIn
		defer func() { confirmIsTerminal, confirmIn = oldT, oldIn }()
		confirmIsTerminal = func() bool { return terminal }
		confirmIn = strings.NewReader(input)
		var b bytes.Buffer
		after, applied, err := headscaleUpPolicy(context.Background(), e.c, newFlags("up"), []byte(pol), policyApplyFlag, yes, &b)
		return string(after), applied, b.String(), err
	}
	for name, c := range map[string]struct {
		terminal bool
		input    string
		code     string
	}{"no terminal": {false, "y\n", "confirmation_required"}, "declined": {true, "n\n", "cancelled"}} {
		_, applied, out, err := run(c.terminal, c.input, false)
		if cliCode(err) != c.code || applied || !strings.Contains(out, `+   "tag:tw-mgmt"`) {
			t.Fatalf("%s: applied=%v err=%v\n%s", name, applied, err, out)
		}
	}
	if _, writes, posts := e.now(); writes != 0 || posts != 0 {
		t.Fatalf("unconfirmed, but %d POSTs", posts)
	}
	after, applied, out, err := run(false, "", true)
	if err != nil || !applied || !strings.Contains(after, "tag:tw-mgmt") {
		t.Fatalf("--yes: applied=%v err=%v\n%s", applied, err, out)
	}
	if _, writes, _ := e.now(); writes != 1 {
		t.Fatalf("--yes: %d writes", writes)
	}
}

// TestUpApplyPolicyAllowAll (issue #389): on a tailnet whose policy allows
// all traffic, tw up --apply-policy adds the tagOwners and only the admin
// console's app grant, not the host ⇄ mgmt network grants.
func TestUpApplyPolicyAllowAll(t *testing.T) {
	const allowAll = `{
  "tagOwners": {"tag:tw": ["autogroup:admin"]},
  "grants": [{"src": ["*"], "dst": ["*"], "ip": ["*"]}],
}`
	e := newUpApplyEnv(t, allowAll)
	after, applied, out, err := e.run(t, false, "", "--yes")
	if err != nil || !applied {
		t.Fatalf("--apply-policy --yes: applied=%v err=%v\n%s", applied, err, out)
	}
	if !strings.Contains(after, mgmt.AdminCap) || !strings.Contains(after, `"tag:tw-mgmt"`) {
		t.Errorf("the admin grant or tag:tw-mgmt owner is missing:\n%s", after)
	}
	if strings.Contains(after, `"tcp:7474"`) || strings.Contains(after, `"src":["tag:tw-host"]`) {
		t.Errorf("host ⇄ mgmt grants written on an allow-all tailnet:\n%s", after)
	}
	if miss, err := policy.Missing([]byte(after), operatorSnippetFor([]byte(after))); err != nil || !miss.Empty() {
		t.Errorf("still missing %s (%v)", miss.HuJSON(), err)
	}
}

// TestTagOwnersFixPasteSafe (issue #389): with the policy known, the fix
// lists only entries to put inside the existing tagOwners section (no
// second "tagOwners" key, no tag:tw it already has), and an existing tag
// keeps its owners.
func TestTagOwnersFixPasteSafe(t *testing.T) {
	t.Parallel()
	pol := []byte(`{"tagOwners": {"tag:tw": ["group:ops"], "tag:tw-host": ["group:ops"]}}`)
	fix := strings.Join(policyMissingTagOwners("why", nil, pol).Fix, "\n")
	for _, want := range []string{`existing "tagOwners" section`, `"tag:tw-mgmt": ["autogroup:admin", "tag:tw"],`, `"tag:tw-host": ["group:ops", "tag:tw"],`} {
		if !strings.Contains(fix, want) {
			t.Errorf("fix lacks %q:\n%s", want, fix)
		}
	}
	if strings.Contains(fix, `"tagOwners": {`) || strings.Contains(fix, `"tag:tw": [`) {
		t.Errorf("fix would add a second tagOwners or tag:tw:\n%s", fix)
	}
	// Policy unknown: the whole block, with the merge note.
	fix = strings.Join(policyMissingTagOwners("why", nil, nil).Fix, "\n")
	if !strings.Contains(fix, `"tagOwners": {`) || !strings.Contains(fix, "don't add a second one") {
		t.Errorf("fix without a policy:\n%s", fix)
	}
}
