package compatflags

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func TestFilter(t *testing.T) {
	if !slices.IsSorted(Experimental) || len(slices.Compact(slices.Clone(Experimental))) != len(Experimental) {
		t.Fatalf("Experimental must be sorted and unique: %v", Experimental)
	}
	got := Filter([]string{"nodejs_compat", "unsafe_module", "nodejs_als", "rtti_api", "unsafe_module", "precise_timers"})
	if want := []string{"precise_timers", "rtti_api", "unsafe_module"}; !slices.Equal(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
	if Filter([]string{"nodejs_compat"}) != nil || Filter(nil) != nil {
		t.Fatal("Filter of stable flags must be nil")
	}
	if !IsExperimental("experimental") || IsExperimental("nodejs_compat") {
		t.Fatal("IsExperimental")
	}
}

func TestInBundle(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01",
			"compatibility_flags":["nodejs_compat","unsafe_module"]}`),
		"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
		"tailworks.jsonc": []byte(`{"tailnet":{"tags":["tag:app"]}}`),
	}
	b, err := bundle.Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	if got := InBundle(b); !slices.Equal(got, []string{"unsafe_module"}) {
		t.Fatalf("InBundle = %v", got)
	}
	if InBundle([]byte("not a bundle")) != nil {
		t.Fatal("InBundle of garbage must be nil")
	}
}

// TestExperimentalRefusedByWorkerd: the pinned workerd, without
// --experimental, refuses every flag in Experimental and accepts the stable
// flags tailworks sets; Probe reports exactly that. A workerd bump that
// stabilises a listed flag fails here (remove it); one that adds an
// experimental flag fails in TestExperimentalMatchesUpstream.
func TestExperimentalRefusedByWorkerd(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	flags := append([]string{"nodejs_compat", "nodejs_als", "no_such_flag_zz"}, Experimental...)
	got, err := Probe(wd, flags)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, Experimental) {
		t.Fatalf("workerd %s refuses as experimental:\n  %v\nthe list says:\n  %v\nonly in workerd: %v\nonly in the list: %v",
			version.WorkerdPin, got, Experimental, only(got, Experimental), only(Experimental, got))
	}
	// Cached: the second call runs no workerd.
	if got2, err := Probe(wd, slices.Clone(flags)); err != nil || !slices.Equal(got2, got) {
		t.Fatalf("cached Probe = %v, %v", got2, err)
	}
	if got, err := Probe(wd, []string{"nodejs_compat"}); err != nil || got != nil {
		t.Fatalf("Probe(stable) = %v, %v", got, err)
	}
	if _, err := Probe(wd, []string{"bad flag"}); err == nil {
		t.Fatal("Probe accepted a bad flag name")
	}
	if _, err := Probe("/no/such/workerd", []string{"unsafe_module"}); err == nil {
		t.Fatal("Probe with no workerd must error")
	}
}

func only(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// upstreamCapnp is where the pinned workerd's compatibility-date.capnp
// lives ($TW_WORKERD_CAPNP points at a local copy instead).
const upstreamCapnp = "https://raw.githubusercontent.com/cloudflare/workerd/v%s/src/workerd/io/compatibility-date.capnp"

var (
	capnpComment = regexp.MustCompile(`#[^\n]*`)
	capnpFlag    = regexp.MustCompile(`compatEnableFlag\("([a-z0-9_]+)"\)`)
)

// parseExperimental returns the $experimental flags of a
// compatibility-date.capnp: each field declaration (up to its `;`) that
// carries the annotation, by its compatEnableFlag name.
func parseExperimental(src []byte) []string {
	var out []string
	for _, field := range strings.Split(capnpComment.ReplaceAllString(string(src), ""), ";") {
		if !strings.Contains(field, "$experimental") {
			continue
		}
		if m := capnpFlag.FindStringSubmatch(field); m != nil {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestParseExperimental(t *testing.T) {
	src := `# a comment with $experimental and compatEnableFlag("not_this")
struct CompatibilityFlags {
  stable @0 :Bool
      $compatEnableFlag("stable_one")
      $compatEnableDate("2024-01-01");
  exp @1 :Bool
      $compatEnableFlag("exp_one")
      $compatDisableFlag("no_exp_one")
      $experimental;
  exp2 @2 :Bool $compatEnableFlag("exp_two") $experimental;
}`
	if got := parseExperimental([]byte(src)); !slices.Equal(got, []string{"exp_one", "exp_two"}) {
		t.Fatalf("parseExperimental = %v", got)
	}
}

// TestExperimentalMatchesUpstream: Experimental equals the $experimental
// flags of the pinned workerd's compatibility-date.capnp (fetched from
// GitHub; skipped offline or with -short). It fails after a workerd bump
// until the list is updated, and prints the diff.
func TestExperimentalMatchesUpstream(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	var src []byte
	if p := os.Getenv("TW_WORKERD_CAPNP"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src = b
	} else {
		url := fmt.Sprintf(upstreamCapnp, version.WorkerdPin)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) || errors.Is(err, context.DeadlineExceeded) || isNetErr(err) {
				t.Skipf("no network: %v", err)
			}
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %s (is workerd %s tagged upstream?)", url, resp.Status, version.WorkerdPin)
		}
		if src, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20)); err != nil {
			t.Fatal(err)
		}
	}
	got := parseExperimental(src)
	if len(got) < 10 {
		t.Fatalf("parsed only %v from compatibility-date.capnp; the file's format may have changed", got)
	}
	if !slices.Equal(got, Experimental) {
		t.Fatalf("Experimental is stale for workerd %s.\nnew upstream (add): %v\nno longer experimental (remove): %v\nupstream's full list:\n  %q",
			version.WorkerdPin, only(got, Experimental), only(Experimental, got), got)
	}
}

func isNetErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne)
}
