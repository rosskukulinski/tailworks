package version

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"v0.2.0", "0.1.9", 1},
		{"0.2.0-1a2b3c4d-dirty", "0.2.0", 0},
		{"1.0.0", "0.99.9", 1},
		{"0.2.1", "0.2.10", -1},
	} {
		if got := MustParse(c.a).Compare(MustParse(c.b)); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "dev", "1.2", "1.2.x", "1.-2.3"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

func TestCheckHost(t *testing.T) {
	for _, c := range []struct {
		host, mgmt string
		ok         bool
	}{
		{"0.2.0", "0.2.3", true},
		{"0.1.4", "0.2.0", true},  // N-1
		{"", "0.2.0", true},       // legacy host = 0.1.0, N-1
		{"", "0.3.0", false},      // legacy host two lines behind
		{"0.1.0", "0.3.0", false}, // N-2
		{"0.3.0", "0.2.0", false}, // host newer than mgmt
		{"0.2.5", "0.2.0", true},  // same line, newer patch
		{"dev", "0.5.0", true},
		{"0.1.0", "dev", true},
		{"1.0.0", "1.1.0", true}, // same major line after 1.0
		{"0.9.0", "1.0.0", true}, // 0.x is the line before 1
		{"1.0.0", "2.0.0", true},
		{"1.4.0", "3.0.0", false},
	} {
		err := CheckHost(c.host, c.mgmt)
		if (err == nil) != c.ok {
			t.Errorf("CheckHost(%q, %q) = %v, want ok=%v", c.host, c.mgmt, err, c.ok)
		}
	}
}

func TestCheckReplicaAndCLI(t *testing.T) {
	if err := CheckReplica("0.1.0", "0.2.0"); err != nil {
		t.Error(err)
	}
	if err := CheckReplica("0.3.0", "0.2.0"); err != nil {
		t.Error(err)
	}
	if err := CheckReplica("0.4.0", "0.2.0"); err == nil {
		t.Error("replicas two lines apart accepted")
	}
	if msg, old := CLIAdvice("0.1.0", "0.3.0"); !old || msg == "" {
		t.Errorf("old CLI: %q %v", msg, old)
	}
	if msg, old := CLIAdvice("0.3.0", "0.2.0"); old || !strings.Contains(msg, "newer") {
		t.Errorf("new CLI: %q %v", msg, old)
	}
	if msg, _ := CLIAdvice("0.2.0", "0.2.1"); msg != "" {
		t.Errorf("same line: %q", msg)
	}
}

func TestWorkerdPinMatchesLockfile(t *testing.T) {
	b, err := os.ReadFile("../../package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if got := lock.Packages["node_modules/workerd"].Version; got != WorkerdPin {
		t.Fatalf("package-lock.json pins workerd %s, internal/version.WorkerdPin is %s: update workerd.go in the same change", got, WorkerdPin)
	}
	if d := WorkerdPinDate(); len(d) != 10 || d[4] != '-' {
		t.Fatalf("WorkerdPinDate() = %q", d)
	}
}
