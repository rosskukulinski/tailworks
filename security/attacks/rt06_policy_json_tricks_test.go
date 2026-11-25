package attacks

// Pass 2 (live) — RT-06, the JSON-parsing tricks the base hostile table
// (internal/policy/hostile_test.go) doesn't cover: a Unicode homoglyph tag
// that visually reads as `tag:tw-*` but isn't ASCII, and a live tenant
// policy whose JSON has duplicate `grants`/`tagOwners` keys (hujson/encoding
// quirks). `--apply-policy` must still only ever add real `tag:tw*` owners
// and tw-sourced grants, and refuse anything else, byte-for-byte.

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

func TestRT06_UnicodeHomoglyphTagRefused(t *testing.T) {
	// "tag:tԝ-wl-x": the 'w' is U+051D (Cyrillic small letter we). It
	// is NOT the ASCII prefix "tag:tw", so Check must refuse it as a
	// non-tw tagOwners key even though it looks like one.
	homo := "tag:tԝ-wl-x"
	if strings.HasPrefix(homo, "tag:tw") {
		t.Fatal("precondition: the homoglyph must not be the ASCII prefix")
	}
	s := policy.Snippet{TagOwners: map[string][]string{homo: {"tag:tw"}}}
	if err := policy.Check(s, policy.Options{}); err == nil {
		t.Fatalf("RT-06: Check accepted a Unicode-homoglyph tagOwners key %q", homo)
	}
	if _, err := policy.Merge([]byte(rt06Base), s); err == nil {
		t.Fatalf("RT-06: Merge accepted a Unicode-homoglyph tag")
	}

	// A grant whose src is the homoglyph tag (not a real tailworks tag) is refused.
	g := policy.Snippet{Grants: []map[string]any{{"src": []string{homo}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}}
	if err := policy.Check(g, policy.Options{}); err == nil {
		t.Fatalf("RT-06: Check accepted a grant src with a homoglyph tag")
	}
}

// RT2-L1 (Low, NEW): policy.Check's tw-namespace test is a bare
// strings.HasPrefix(tag, "tag:tw") with no delimiter or whitespace check,
// so a src/tagOwners tag with a trailing space (or other trailing bytes)
// such as "tag:tw-wl-x " is accepted as a tailworks tag. Impact is bounded —
// the accepted string is still inside tailworks' own tag:tw* namespace, grants
// to a space-padded tag match no real device, and every policy write is
// validated by Tailscale before apply (docs/install-testing.md safety rule 2) — so this is
// input-validation leniency, not a cross-principal widening. Fix: require a
// boundary after the prefix (exact "tag:tw" or "tag:tw-"...) and reject
// tags containing whitespace/control characters. Skip-marked until fixed.
func TestRT2L1_CheckAcceptsWhitespacePaddedTag(t *testing.T) {
	for _, bad := range []string{"tag:tw-wl-x ", "tag:tw-wl-x\t", "tag:tw-wl-x\n", "tag:twx", "tag:tw-", "tag:tw-a\x00b"} {
		bs := policy.Snippet{Grants: []map[string]any{{"src": []string{bad}, "dst": []string{"tag:tw-wl-x"}, "ip": []string{"tcp:80"}}}}
		if err := policy.Check(bs, policy.Options{}); err == nil {
			t.Fatalf("RT2-L1: Check accepted a whitespace-padded src %q", bad)
		}
	}
}

func TestRT06_DuplicateKeyPolicyStaysAdditive(t *testing.T) {
	// A live tenant policy whose JSON repeats "grants" and "tagOwners".
	// Merging a legitimate tailworks snippet must not crash, must stay additive,
	// and must not silently drop or widen the tenant's own grants.
	dup := `{
      "tagOwners": {"tag:ttest": ["autogroup:admin"]},
      "tagOwners": {"tag:prod": ["autogroup:admin"]},
      "grants": [{"src": ["tag:ttest"], "dst": ["tag:ttest"], "ip": ["*"]}],
      "grants": [{"src": ["tag:prod"], "dst": ["tag:prod"], "ip": ["*"]}]
    }`
	good := policy.Snippet{
		TagOwners: map[string][]string{"tag:tw-wl-x": {"tag:tw"}},
		Grants:    []map[string]any{{"src": []string{"tag:tw-wl-x"}, "dst": []string{"tag:db"}, "ip": []string{"tcp:80"}}},
	}
	out, err := policy.Merge([]byte(dup), good)
	if err != nil {
		// Refusing a malformed/duplicate-key policy is an acceptable, safe outcome.
		t.Logf("RT-06: Merge refused a duplicate-key policy (safe): %v", err)
		return
	}
	// If it merged, it must be verifiably additive over the parsed `before`.
	if err := policy.VerifyAdditive([]byte(dup), out, good); err != nil {
		t.Fatalf("RT-06: duplicate-key merge was not additive: %v", err)
	}
	// And it must not have introduced a non-tw tagOwners or a `*` src.
	so := string(out)
	if strings.Contains(so, `"src":["*"]`) || strings.Contains(so, `"src": ["*"]`) {
		t.Fatalf("RT-06: merge widened src to * :\n%s", so)
	}
}

const rt06Base = `{"tagOwners":{"tag:ttest":["autogroup:admin"]},"grants":[{"src":["tag:ttest"],"dst":["tag:ttest"],"ip":["*"]}]}`
