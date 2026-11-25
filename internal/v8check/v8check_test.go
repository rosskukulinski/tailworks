package v8check

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestMilestone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v8   string
		want int
		ok   bool
	}{
		{"15.8.62.5", 158, true},
		{"14.2.231.17", 142, true},
		{"16.0.1.0", 160, true},
		{" 13.9.100.3\n", 139, true},
		{"15.8.62", 0, false},   // three parts
		{"15.12.1.2", 0, false}, // MINOR is one digit
		{"9.9.1.1", 0, false},   // MAJOR is two digits
		{"10.0.0.1", 0, false},  // V8 10 is 2022; this shape is an IP in a binary
		{"158.0.8127.88", 0, false},
		{"", 0, false},
	} {
		got, err := Milestone(c.v8)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("Milestone(%q) = %d, %v; want %d, ok=%v", c.v8, got, err, c.want, c.ok)
		}
	}
	if MilestoneV8(158) != "15.8" || MilestoneV8(160) != "16.0" {
		t.Errorf("MilestoneV8: %s %s", MilestoneV8(158), MilestoneV8(160))
	}
}

func TestChromeMilestone(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v    string
		want int
		ok   bool
	}{
		{"158.0.8127.88", 158, true},
		{"142.0.7444.59", 142, true},
		{"158", 0, false},
		{"15.8.62.5", 0, false},
		{"x.1.2.3", 0, false},
	} {
		got, err := ChromeMilestone(c.v)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ChromeMilestone(%q) = %d, %v; want %d, ok=%v", c.v, got, err, c.want, c.ok)
		}
	}
}

// TestWorkerdV8 scans a stand-in for the binary: the version string sits
// NUL-terminated among other strings, some of them version-shaped but not
// V8's (a four-part Chrome version, a three-part version, a date).
func TestWorkerdV8(t *testing.T) {
	t.Parallel()
	bin := func(strs ...string) []byte {
		var b bytes.Buffer
		b.Write([]byte{0x7f, 'E', 'L', 'F', 0, 0, 1})
		for _, s := range strs {
			b.WriteString(s)
			b.WriteByte(0)
		}
		b.Write(bytes.Repeat([]byte{0xff, 0}, 16))
		return b.Bytes()
	}
	v, err := WorkerdV8(bytes.NewReader(bin("workerd 2026-11-21", "15.8.62.5", "158.0.8127.88", "1.20261121.1", "22.23.4", "v8_enable_sandbox: 0,", "10.0.0.1", "127.0.0.1", "see 15.1.3.4 in the docs")))
	if err != nil || v != "15.8.62.5" {
		t.Fatalf("WorkerdV8 = %q, %v", v, err)
	}
	// Two standalone V8-shaped strings are reported, not guessed between.
	if _, err := WorkerdV8(bytes.NewReader(bin("15.8.62.5", "15.1.3.4"))); err == nil || !strings.Contains(err.Error(), "15.1.3.4") {
		t.Fatalf("two candidates: %v", err)
	}
	if _, err := WorkerdV8(bytes.NewReader(bin("workerd 2026-11-21", "no version here"))); err == nil {
		t.Fatal("no candidate: want an error")
	}
	// The same version string twice is one version.
	if v, err := WorkerdV8(bytes.NewReader(bin("15.8.62.5", "other", "15.8.62.5"))); err != nil || v != "15.8.62.5" {
		t.Fatalf("duplicate: %q, %v", v, err)
	}
}

func TestParseChromiumDash(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/chromiumdash-stable.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseChromiumDash(b)
	if err != nil || c.Version != "158.0.8127.88" || c.Milestone != 158 || c.Channel != "Stable" || c.Date() != "2026-11-23" {
		t.Fatalf("ParseChromiumDash: %+v, %v", c, err)
	}
	if _, err := ParseChromiumDash([]byte(`[]`)); err == nil {
		t.Fatal("empty: want an error")
	}
	if _, err := ParseChromiumDash([]byte(`[{"version":"158.0.8127.88","milestone":157}]`)); err == nil {
		t.Fatal("milestone/version disagreement: want an error")
	}
	if c, err := ParseChromiumDash([]byte(`[{"version":"159.0.1.2"}]`)); err != nil || c.Milestone != 159 || c.Date() != "" {
		t.Fatalf("milestone from the version: %+v, %v", c, err)
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	stable := ChromeRelease{Version: "158.0.8127.88", Milestone: 158, Time: 1795466737426}
	r, err := Compare("1.20261121.1", "15.8.62.5", stable)
	if err != nil || r.Behind || r.WorkerdMilestone != 158 || r.ChromeMilestone != 158 {
		t.Fatalf("same milestone: %+v, %v", r, err)
	}
	if s := r.Summary(); !strings.HasPrefix(s, "ok: ") || !strings.Contains(s, "15.8.62.5") || !strings.Contains(s, "158.0.8127.88 (2026-11-23)") {
		t.Errorf("summary: %q", s)
	}
	r, err = Compare("1.20261118.1", "15.7.200.1", stable)
	if err != nil || !r.Behind {
		t.Fatalf("behind: %+v, %v", r, err)
	}
	if s := r.Summary(); !strings.HasPrefix(s, "BEHIND: ") || !strings.Contains(s, "V8 15.8") {
		t.Errorf("summary: %q", s)
	}
	body := r.IssueBody("https://github.com/x/y/actions/runs/1")
	for _, want := range []string{"1.20261118.1", "15.7.200.1", "Chrome **157**", "158.0.8127.88", "V8 **15.8**", ChromeReleasesURL, "actions/runs/1", "#338", "7 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("issue body lacks %q:\n%s", want, body)
		}
	}
	// Ahead of stable (workerd tracks V8 beta at times) is not behind.
	if r, err := Compare("1.20261121.1", "15.9.1.1", stable); err != nil || r.Behind {
		t.Fatalf("ahead: %+v, %v", r, err)
	}
	if _, err := Compare("1.20261121.1", "workerd 2026-11-21", stable); err == nil {
		t.Fatal("bad V8 version: want an error")
	}
	if _, err := Compare("1.20261121.1", "15.8.62.5", ChromeRelease{Version: "stable"}); err == nil {
		t.Fatal("bad Chrome version: want an error")
	}
}
