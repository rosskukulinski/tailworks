package replicaname

import (
	"math/rand"
	"regexp"
	"testing"
)

func TestName(t *testing.T) {
	for _, c := range []struct {
		w    string
		i    int
		want string
	}{{"web", 0, "web"}, {"web", 1, "web--r2"}, {"web", 2, "web--r3"}, {"web", 15, "web--r16"}} {
		if got := Name(c.w, c.i); got != c.want {
			t.Errorf("Name(%q, %d) = %q, want %q", c.w, c.i, got, c.want)
		}
	}
}

func TestGroup(t *testing.T) {
	for _, c := range []struct {
		name, group string
		ok          bool
	}{
		// Replica names, as mgmt makes them, and every other n ≥ 2.
		{"web--r2", "web", true},
		{"web--r9", "web", true},
		{"web--r10", "web", true},
		{"web--r16", "web", true},
		{"web--r17", "web", true},
		{"web--r100", "web", true},
		{"api--r3", "api", true},
		{"a--r2--r3", "a--r2", true}, // only the last suffix counts
		{"a---r2", "a-", true},
		{"web--preview-x--r2", "web--preview-x", true},
		// Not replica names.
		{"web", "", false},
		{"web--r", "", false},
		{"web--r0", "", false},
		{"web--r1", "", false}, // replica 1 is the workload itself
		{"web--r02", "", false},
		{"web--r2x", "", false},
		{"web--rx", "", false},
		{"web-r2", "", false},
		{"web--R2", "", false},
		{"web--r2-", "", false},
		{"web--r+2", "", false},
		{"--r2", "", false}, // no group
		{"r2", "", false},
		{"", "", false},
	} {
		if g, ok := Group(c.name); g != c.group || ok != c.ok {
			t.Errorf("Group(%q) = %q, %v; want %q, %v", c.name, g, ok, c.group, c.ok)
		}
	}
	// Every name Name makes for a replica is one Group recognises, and gives
	// its workload back.
	for i := 1; i < 40; i++ {
		if g, ok := Group(Name("svc", i)); !ok || g != "svc" {
			t.Errorf("Group(Name(svc, %d)) = %q, %v", i, g, ok)
		}
	}
}

// TestGroupMatchesHostdBlobPrefix: before #348 hostd found a replica's R2
// blob prefix with this expression (hostd/r2.go). Group must strip exactly
// what it stripped, for any name, so no replica's existing blobs move.
func TestGroupMatchesHostdBlobPrefix(t *testing.T) {
	legacyRE := regexp.MustCompile(`--r([2-9]|[1-9][0-9]+)$`)
	legacy := func(name string) string {
		if g := legacyRE.ReplaceAllString(name, ""); g != "" {
			return g
		}
		return name
	}
	now := func(name string) string {
		if g, ok := Group(name); ok {
			return g
		}
		return name
	}
	const alphabet = "abr-0129x"
	rng := rand.New(rand.NewSource(348))
	for n := 0; n < 200000; n++ {
		b := make([]byte, rng.Intn(12))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		if name := string(b); legacy(name) != now(name) {
			t.Fatalf("%q: hostd stripped it to %q, Group gives %q", name, legacy(name), now(name))
		}
	}
}

func FuzzGroup(f *testing.F) {
	for _, s := range []string{"web--r2", "web--r1", "--r2", "a--r2--r3", "web--r02", "web--r99999999999999999999999"} {
		f.Add(s)
	}
	legacyRE := regexp.MustCompile(`--r([2-9]|[1-9][0-9]+)$`)
	f.Fuzz(func(t *testing.T, name string) {
		g, ok := Group(name)
		want := legacyRE.ReplaceAllString(name, "")
		if matched := legacyRE.MatchString(name) && want != ""; matched != ok || (ok && g != want) {
			t.Fatalf("Group(%q) = %q, %v; the expression gives %q (matched %v)", name, g, ok, want, matched)
		}
		if ok && (g == "" || g+name[len(g):] != name) {
			t.Fatalf("Group(%q) = %q is not a prefix of the name", name, g)
		}
	})
}
