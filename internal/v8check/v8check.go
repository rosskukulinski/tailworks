// Package v8check compares the V8 inside the pinned workerd with the V8 in
// Chrome's stable channel (issue #338; docs/RELEASING.md "Upstream
// advisories").
//
// V8 security fixes don't arrive as workerd advisories: they arrive as V8
// rolls inside ordinary workerd releases, and the public signal is Chrome's
// stable-channel security release, whose notes list the V8 CVEs. A V8
// version MAJOR.MINOR ships in Chrome milestone MAJOR*10+MINOR (V8 15.8 is
// Chrome 158), so the pin is behind when its V8 milestone is lower than the
// milestone Chrome stable runs.
//
// The pinned workerd's V8 version is read from the workerd binary: V8 embeds
// its version string (v8::V8::GetVersion, "15.8.62.5"), and that is the
// only place it shows. process.versions.v8 is "" under the pinned workerd's
// nodejs_compat (measured on 1.20261121.1), workerd --version prints only
// its date, and the inspector's /json/version names no V8.
package v8check

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ChromiumDashURL is Chrome's current stable release (one entry; Linux, so
// no Android or iOS skew).
const ChromiumDashURL = "https://chromiumdash.appspot.com/fetch_releases?channel=Stable&platform=Linux&num=1"

// ChromeReleasesURL lists the stable-channel release notes, which name the
// V8 CVEs each release fixes.
const ChromeReleasesURL = "https://chromereleases.googleblog.com/search/label/Stable%20updates"

// IssueTitle is the one issue the nightly job opens, updates and closes.
const IssueTitle = "workerd pin is behind Chrome stable's V8"

// reV8 is a V8 version string on its own: MAJOR.MINOR.BUILD.PATCH, with
// MINOR a single digit (V8 15.8, 16.0, …) and MAJOR 12 to 19: V8 12 is
// 2024, and the lower bound keeps IP-shaped strings (10.0.0.1) out of the
// binary scan.
var reV8 = regexp.MustCompile(`^(1[2-9])\.([0-9])\.([0-9]{1,3})\.([0-9]{1,3})$`)

// Milestone is the Chrome milestone a V8 version belongs to: V8
// MAJOR.MINOR ships in Chrome MAJOR*10+MINOR.
func Milestone(v8 string) (int, error) {
	m := reV8.FindStringSubmatch(strings.TrimSpace(v8))
	if m == nil {
		return 0, fmt.Errorf("%q is not a V8 version (MAJOR.MINOR.BUILD.PATCH)", v8)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major*10 + minor, nil
}

// MilestoneV8 is the V8 MAJOR.MINOR Chrome milestone m carries ("15.8" for
// 158).
func MilestoneV8(m int) string {
	return fmt.Sprintf("%d.%d", m/10, m%10)
}

// ChromeMilestone is the milestone of a Chrome version ("158.0.8127.88" →
// 158).
func ChromeMilestone(v string) (int, error) {
	first, _, ok := strings.Cut(strings.TrimSpace(v), ".")
	m, err := strconv.Atoi(first)
	if !ok || err != nil || m < 100 || m > 999 {
		return 0, fmt.Errorf("%q is not a Chrome version (MAJOR.MINOR.BUILD.PATCH)", v)
	}
	return m, nil
}

// WorkerdV8 scans a workerd binary for V8's version string. It is the one
// printable NUL-terminated string of the form MAJOR.MINOR.BUILD.PATCH with a
// two-digit MAJOR; it fails when there is none or more than one, rather than
// guess.
func WorkerdV8(r io.Reader) (string, error) {
	const maxRun = 64
	found := map[string]bool{}
	br := bufio.NewReaderSize(r, 1<<20)
	run := make([]byte, 0, maxRun+1)
	flush := func() {
		if len(run) > 0 && len(run) <= maxRun && reV8.Match(run) {
			found[string(run)] = true
		}
		run = run[:0]
	}
	for {
		b, err := br.ReadByte()
		if err != nil {
			flush()
			if err != io.EOF {
				return "", err
			}
			break
		}
		if b >= 0x20 && b < 0x7f {
			if len(run) <= maxRun {
				run = append(run, b)
			}
			continue
		}
		flush()
	}
	switch len(found) {
	case 0:
		return "", errors.New("no V8 version string in the workerd binary")
	case 1:
		for v := range found {
			return v, nil
		}
	}
	var all []string
	for v := range found {
		all = append(all, v)
	}
	sort.Strings(all)
	return "", fmt.Errorf("more than one V8-shaped version string in the workerd binary: %s", strings.Join(all, ", "))
}

// ChromeRelease is one entry of chromiumdash's fetch_releases.
type ChromeRelease struct {
	Channel   string `json:"channel"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Milestone int    `json:"milestone"`
	Time      int64  `json:"time"` // ms since the epoch
}

// Date is the release's day, UTC.
func (c ChromeRelease) Date() string {
	if c.Time == 0 {
		return ""
	}
	return time.UnixMilli(c.Time).UTC().Format("2006-01-02")
}

// ParseChromiumDash reads the first entry of a fetch_releases answer and
// checks its version and milestone agree.
func ParseChromiumDash(b []byte) (ChromeRelease, error) {
	var rels []ChromeRelease
	if err := json.Unmarshal(b, &rels); err != nil {
		return ChromeRelease{}, fmt.Errorf("chromiumdash: %v", err)
	}
	if len(rels) == 0 {
		return ChromeRelease{}, errors.New("chromiumdash: no releases in the answer")
	}
	c := rels[0]
	m, err := ChromeMilestone(c.Version)
	if err != nil {
		return ChromeRelease{}, fmt.Errorf("chromiumdash: %v", err)
	}
	if c.Milestone == 0 {
		c.Milestone = m
	}
	if c.Milestone != m {
		return ChromeRelease{}, fmt.Errorf("chromiumdash: version %s but milestone %d", c.Version, c.Milestone)
	}
	return c, nil
}

// Result is one comparison of the pin with Chrome stable.
type Result struct {
	Workerd          string `json:"workerd"`          // the pinned workerd (1.YYYYMMDD.N)
	WorkerdV8        string `json:"workerdV8"`        // its V8 version
	WorkerdMilestone int    `json:"workerdMilestone"` // the Chrome milestone that V8 belongs to
	Chrome           string `json:"chrome"`           // Chrome stable's version
	ChromeDate       string `json:"chromeDate"`       // its release day (UTC), "" when unknown
	ChromeMilestone  int    `json:"chromeMilestone"`
	Behind           bool   `json:"behind"` // the pin's V8 milestone is lower than Chrome stable's
}

// Compare says whether the pinned workerd's V8 is behind Chrome stable's.
func Compare(workerd, workerdV8 string, chrome ChromeRelease) (Result, error) {
	wm, err := Milestone(workerdV8)
	if err != nil {
		return Result{}, err
	}
	cm, err := ChromeMilestone(chrome.Version)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Workerd:          workerd,
		WorkerdV8:        workerdV8,
		WorkerdMilestone: wm,
		Chrome:           chrome.Version,
		ChromeDate:       chrome.Date(),
		ChromeMilestone:  cm,
		Behind:           wm < cm,
	}, nil
}

// Summary is the one-line result.
func (r Result) Summary() string {
	chrome := r.Chrome
	if r.ChromeDate != "" {
		chrome += " (" + r.ChromeDate + ")"
	}
	if r.Behind {
		return fmt.Sprintf("BEHIND: workerd %s carries V8 %s (Chrome %d); Chrome stable is %s, V8 %s", r.Workerd, r.WorkerdV8, r.WorkerdMilestone, chrome, MilestoneV8(r.ChromeMilestone))
	}
	return fmt.Sprintf("ok: workerd %s carries V8 %s (Chrome %d); Chrome stable is %s", r.Workerd, r.WorkerdV8, r.WorkerdMilestone, chrome)
}

// IssueBody is the body of the issue the nightly job files when the pin is
// behind. runURL ("" for none) is the workflow run that found it.
func (r Result) IssueBody(runURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The pinned workerd **%s** carries V8 **%s**, which belongs to Chrome **%d**. Chrome's stable channel is **%s**", r.Workerd, r.WorkerdV8, r.WorkerdMilestone, r.Chrome)
	if r.ChromeDate != "" {
		fmt.Fprintf(&b, " (released %s)", r.ChromeDate)
	}
	fmt.Fprintf(&b, ", which carries V8 **%s**.\n\n", MilestoneV8(r.ChromeMilestone))
	fmt.Fprintf(&b, "Chrome's stable release notes name the V8 CVEs each release fixes: %s\n\n", ChromeReleasesURL)
	b.WriteString("This issue starts the clock in docs/RELEASING.md (\"Upstream advisories\", V8): triage the notes within 2 business days; a High or Critical V8 CVE reachable from tenant code means a patch release with a workerd that carries the fixed V8 within 7 days of the Chrome stable release that names it.\n\n")
	b.WriteString("To catch up: `npm install workerd@latest`, set `WorkerdPin` in `internal/version/workerd.go` to the same version, add a `workerd` release-note fragment, and run `make lint test` and `make e2e-compat`. The next nightly `v8-check` closes this issue when the pin's V8 milestone reaches Chrome stable's.\n\n")
	fmt.Fprintf(&b, "Opened by `make v8-check` in .github/workflows/nightly.yml (issue #338)")
	if runURL != "" {
		fmt.Fprintf(&b, ": %s", runURL)
	}
	b.WriteString("\n")
	return b.String()
}
