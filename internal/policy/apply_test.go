package policy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// racyAPI is a policy API someone else edits too (issue #116): each of the
// first `conflicts` writes loses the race. Before refusing it, a concurrent
// editor changes the policy (edit) and its ETag, as the admin console would.
type racyAPI struct {
	body      []byte
	etag      string
	conflicts int
	edit      func([]byte) []byte
	sets      int      // writes attempted
	matches   []string // their If-Match
	written   int      // writes that landed
}

func (f *racyAPI) GetPolicyHuJSON(context.Context) ([]byte, string, error) {
	return append([]byte(nil), f.body...), f.etag, nil
}
func (f *racyAPI) ValidatePolicy(context.Context, []byte) error { return nil }
func (f *racyAPI) SetPolicy(_ context.Context, b []byte, m string) (string, error) {
	f.sets++
	f.matches = append(f.matches, m)
	if f.conflicts > 0 {
		f.conflicts--
		if f.edit != nil {
			f.body = f.edit(f.body)
		}
		f.etag += "+"
		return "", &tsapi.APIError{Status: http.StatusPreconditionFailed, Message: "precondition failed"}
	}
	if m != f.etag {
		return "", &tsapi.APIError{Status: http.StatusPreconditionFailed, Message: "stale If-Match"}
	}
	f.body, f.etag = b, f.etag+"'"
	f.written++
	return f.etag, nil
}

// appSnippet is what tw policy --apply writes for a typical app.
func appSnippet() Snippet {
	return Snippet{
		TagOwners: map[string][]string{"tag:tw-wl-x": {"autogroup:admin", "tag:tw"}},
		Grants:    []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"tag:db"}, "ip": []string{"tcp:80"}}},
	}
}

// concurrentEdit is a person adding their own grant while tw writes.
func concurrentEdit(b []byte) []byte {
	return []byte(strings.Replace(string(b), `"grants": [`, `"grants": [
		{"src": ["alice@example.com"], "dst": ["tag:ttest"], "ip": ["tcp:22"]}, // alice's`, 1))
}

// An ETag conflict is retried once, from a fresh read: the concurrent edit
// is kept, the snippet is added, and nothing existing is removed.
func TestApplyRetryConflictOnce(t *testing.T) {
	f := &racyAPI{body: []byte(base), etag: `"e1"`, conflicts: 1, edit: concurrentEdit}
	r, err := ApplyRetry(t.Context(), f, appSnippet(), Options{})
	if err != nil || !r.Changed || f.sets != 2 || f.written != 1 {
		t.Fatalf("ApplyRetry: %+v err=%v sets=%d written=%d", r, err, f.sets, f.written)
	}
	if f.matches[0] != `"e1"` || f.matches[1] != `"e1"+` {
		t.Fatalf("If-Match %v: the retry must use the re-read ETag", f.matches)
	}
	got := string(f.body)
	for _, want := range []string{"// my tailnet", "// keep me", "// alice's", `"alice@example.com"`, `"tag:tw-wl-x"`, `"tag:db"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("after the retry the policy lacks %q:\n%s", want, got)
		}
	}
	// The write is the edited policy plus the snippet, nothing else.
	if err := VerifyAdditive(concurrentEdit([]byte(base)), f.body, appSnippet()); err != nil {
		t.Fatal(err)
	}
}

// A second conflict fails with ErrConflict: no third attempt, the policy
// is exactly what the other editor left.
func TestApplyRetryConflictTwice(t *testing.T) {
	f := &racyAPI{body: []byte(base), etag: `"e1"`, conflicts: 5, edit: concurrentEdit}
	_, err := ApplyRetry(t.Context(), f, appSnippet(), Options{})
	if !errors.Is(err, ErrConflict) || f.sets != 2 || f.written != 0 {
		t.Fatalf("want ErrConflict after exactly 2 writes: err=%v sets=%d written=%d", err, f.sets, f.written)
	}
	if strings.Contains(string(f.body), "tag:tw-wl-x") {
		t.Fatalf("a refused write landed:\n%s", f.body)
	}
	// Any other failure is not retried.
	g := &fakeAPI{body: []byte(base), etag: `"e1"`, validErr: errors.New("policy invalid: tag:db not defined")}
	if _, err := ApplyRetry(t.Context(), g, appSnippet(), Options{}); err == nil || errors.Is(err, ErrConflict) || g.sets != 0 {
		t.Fatalf("validation failure: err=%v sets=%d", err, g.sets)
	}
}

// RT-06 through the retry path: a hostile snippet is refused before the
// first read, so neither attempt writes; a concurrent edit that removes
// existing entries is never undone or extended beyond the snippet.
func TestApplyRetryHostile(t *testing.T) {
	hostile := map[string]Snippet{
		"non-tw tagOwners":     {TagOwners: map[string][]string{"tag:prod": {"tag:tw"}}},
		"tw tag owned by user": {TagOwners: map[string][]string{"tag:tw-wl-x": {"alice@example.com"}}},
		"tw lookalike tag":     {TagOwners: map[string][]string{"tag:twprod": {"tag:tw"}}},
		"src non-tw tag":       {Grants: []map[string]any{{"src": []string{"tag:prod"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}},
		"src *":                {Grants: []map[string]any{{"src": []string{"*"}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"*"}}}},
		"dst *":                {Grants: []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"*"}, "ip": []string{"*"}}}},
		"funnel on workload":   {NodeAttrs: []map[string]any{{"target": []string{"tag:tw-wl-x"}, "attr": []string{"funnel"}}}},
		"good + hostile grant": {TagOwners: appSnippet().TagOwners, Grants: append(appSnippet().Grants, map[string]any{"src": []string{"group:eng"}, "dst": []string{"tag:tw-wl-x"}})},
	}
	for name, s := range hostile {
		t.Run(name, func(t *testing.T) {
			f := &racyAPI{body: []byte(base), etag: `"e1"`, conflicts: 1, edit: concurrentEdit}
			if _, err := ApplyRetry(t.Context(), f, s, Options{}); err == nil || f.sets != 0 || string(f.body) != base {
				t.Fatalf("ApplyRetry wrote a hostile snippet: err=%v sets=%d\n%s", err, f.sets, f.body)
			}
		})
	}
	// The other editor removes the existing grant during the race: the
	// retry merges into what is there now and puts nothing back.
	remove := func([]byte) []byte {
		return []byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]}, "grants": []}`)
	}
	f := &racyAPI{body: []byte(base), etag: `"e1"`, conflicts: 1, edit: remove}
	if _, err := ApplyRetry(t.Context(), f, appSnippet(), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdditive(remove(nil), f.body, appSnippet()); err != nil {
		t.Fatalf("retry result is not the current policy plus the snippet: %v\n%s", err, f.body)
	}
	if strings.Contains(string(f.body), `"dst": ["tag:ttest"]`) || strings.Contains(string(f.body), `"dst":["tag:ttest"]`) {
		t.Fatalf("the retry restored a grant someone removed:\n%s", f.body)
	}
}

func TestSnippetDiff(t *testing.T) {
	if d := (Snippet{}).Diff(); d != nil {
		t.Fatalf("empty diff %v", d)
	}
	d := appSnippet().Diff()
	if len(d) == 0 || !strings.HasPrefix(d[0], "+ \"tagOwners\"") {
		t.Fatalf("diff %q", d)
	}
	for _, l := range d {
		if !strings.HasPrefix(l, "+ ") {
			t.Fatalf("diff line %q is not an addition", l)
		}
	}
}
