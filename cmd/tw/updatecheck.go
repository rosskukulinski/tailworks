package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rosskukulinski/tailworks/internal/catalog"
	"github.com/rosskukulinski/tailworks/internal/esbundle"
	twversion "github.com/rosskukulinski/tailworks/internal/version"
)

// The update check (issue #46): `tw version --check` compares this tailworks
// with the latest published GitHub release (the one scripts/get.sh
// installs), prints how to upgrade, and says whether the daemons `tw up`
// started run an older binary and need a restart. Releases are tagged
// v<semver> (docs/INSTALL.md); TW_REPO names another repository, as for
// get.sh, and TW_RELEASE_API another API base (tests, mirrors).
//
// After a command, an interactive tailworks also prints a one-line notice when a
// newer release is out, at most once a day. It is off with --json, when
// stderr isn't a terminal, with CI set, for development builds, and with
// TW_NO_UPDATE_CHECK=1. It never runs after the local-only commands in
// noUpdateNotice (tw init, tw dev: no tailnet, no credentials, no outbound
// request; issue #104). `tw version --json` reports it as updateCheck.

// versionCheck is `tw version --check --json` (schema $defs/version).
type versionCheck struct {
	Latest          string         `json:"latest"`
	URL             string         `json:"url,omitempty"`
	UpdateAvailable bool           `json:"updateAvailable"`
	Upgrade         string         `json:"upgrade,omitempty"` // the command that installs it
	Daemons         *daemonVersion `json:"daemons"`           // null: no tw up running on this machine

	// Security: the latest release's notes have a Security section (a
	// `security` fragment shipped in it; issue #338).
	Security bool `json:"securityRelease"`
}

// daemonVersion is what the running tw up reports (GET /v1/status).
type daemonVersion struct {
	Version        string `json:"version"`
	RestartNeeded  bool   `json:"restartNeeded"`
	RestartCommand string `json:"restartCommand,omitempty"`
}

type latestRelease struct {
	Tag string `json:"tag_name"`
	URL string `json:"html_url"`

	// Body is the release notes (scripts/release-notes.sh); a Security
	// section in it marks a security release (catalog.HasSecurityNotes).
	Body string `json:"body"`
}

func releaseRepo() string {
	if r := os.Getenv("TW_REPO"); r != "" {
		return r
	}
	return "rosskukulinski/tailworks"
}

// releaseAPI is the GitHub API base every "latest release" lookup uses
// (tw version --check, tw upgrade): TW_RELEASE_API names a mirror or a
// test server.
func releaseAPI() string {
	if api := strings.TrimSuffix(os.Getenv("TW_RELEASE_API"), "/"); api != "" {
		return api
	}
	return "https://api.github.com"
}

// fetchLatestRelease asks GitHub for the latest published release.
func fetchLatestRelease(ctx context.Context) (latestRelease, error) {
	var r latestRelease
	req, err := http.NewRequestWithContext(ctx, "GET", releaseAPI()+"/repos/"+releaseRepo()+"/releases/latest", nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tw/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return r, fail("update_check_failed", "can't reach GitHub to find the latest tw release: "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return r, fail("update_check_failed", fmt.Sprintf("GitHub answered %s for the latest release of %s", resp.Status, releaseRepo()))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil || r.Tag == "" {
		return r, fail("update_check_failed", "no published release found in "+releaseRepo())
	}
	return r, nil
}

// semver is a version parsed the way ADR-041 does (internal/version):
// MAJOR.MINOR.PATCH, with any -suffix (a commit, "-dev", "-dirty") kept in
// pre and ignored by comparisons; ok is false for "dev" and anything else
// unparseable.
type semver struct {
	v   twversion.Semver
	pre string
	ok  bool
}

func parseSemver(s string) semver {
	if twversion.IsDev(s) {
		return semver{}
	}
	v, err := twversion.Parse(s)
	if err != nil {
		return semver{}
	}
	return semver{v: v, pre: v.Suffix, ok: true}
}

// newerThan reports whether a is a later release than b (suffixes ignored,
// as in internal/version).
func (a semver) newerThan(b semver) bool { return a.v.Compare(b.v) > 0 }

// restartCommand is how to restart the daemons tw up started.
func restartCommand(s *state) string {
	if serviceIsFor(s) { // service.go
		return serviceRestartHint()
	}
	return "tw down, then the same tw up command as before"
}

// runningDaemons is the version the local tw up's mgmt reports, nil when
// none runs here (or this machine is only linked).
func runningDaemons(ctx context.Context, s *state) *daemonVersion {
	if s == nil || s.remote() || !s.configured() || !s.supervisorAlive() {
		return nil
	}
	var st struct {
		Version string `json:"version"`
	}
	if err := s.mgmt().Do(ctx, "GET", "/v1/status", nil, &st); err != nil || st.Version == "" {
		return nil
	}
	d := &daemonVersion{Version: st.Version}
	if st.Version != version {
		d.RestartNeeded = true
		d.RestartCommand = restartCommand(s)
	}
	return d
}

// cmdVersionCheck is `tw version --check`.
func cmdVersionCheck(f *flags) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	res := versionCheck{Latest: latest, URL: rel.URL, Security: catalog.HasSecurityNotes(rel.Body)}
	cur, lv := parseSemver(version), parseSemver(latest)
	// A build from source (dev, 0.2.0-dev) isn't compared: it may be ahead
	// of the latest release or behind it.
	release := cur.ok && cur.pre == ""
	res.UpdateAvailable = release && lv.ok && lv.newerThan(cur)
	if res.UpdateAvailable {
		res.Upgrade = upgradeCommand(latest) // install_channel.go
	}
	s, _ := openState(*f.stateDir)
	res.Daemons = runningDaemons(ctx, s)
	saveUpdateCache(s, latest, res.Security)
	emit(f, map[string]any{"tw": version, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH, "workerdPin": workerdPin, "esbuild": esbundle.Version,
		"latest": res.Latest, "url": res.URL, "updateAvailable": res.UpdateAvailable, "securityRelease": res.Security,
		"upgrade": res.Upgrade, "daemons": res.Daemons, "updateCheck": updateCheckOn()}, func() { printVersionCheck(res, release) })
	return nil
}

func printVersionCheck(r versionCheck, release bool) {
	switch {
	case !release:
		fmt.Printf("this tailworks (%s) is a development build; the latest release is %s\n", version, r.Latest)
	case r.UpdateAvailable:
		fmt.Printf("tw %s is out (this is %s)%s: %s\n", r.Latest, version, securityReleaseTag(r.Security), r.URL)
		fmt.Printf("  upgrade: %s\n", r.Upgrade)
	default:
		fmt.Printf("✓ tw %s is the latest release\n", version)
	}
	switch d := r.Daemons; {
	case d == nil:
	case d.RestartNeeded:
		fmt.Printf("! the running tw up is %s, this binary is %s: restart it (%s)\n", d.Version, version, d.RestartCommand)
	default:
		fmt.Printf("✓ the running tw up is %s too\n", d.Version)
	}
}

// ---- the once-a-day notice ----

// updateCache is <state>/update-check.json.
type updateCache struct {
	CheckedAt  time.Time `json:"checkedAt"`
	Latest     string    `json:"latest"`
	NotifiedAt time.Time `json:"notifiedAt,omitempty"`

	// Security: Latest's notes have a Security section (issue #338).
	Security bool `json:"securityRelease,omitempty"`
}

const updateEvery = 24 * time.Hour

func updateCachePath(dir string) string { return filepath.Join(dir, "update-check.json") }

func loadUpdateCache(dir string) updateCache {
	var c updateCache
	if b, err := os.ReadFile(updateCachePath(dir)); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func (c updateCache) save(dir string) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return // never create the state dir for this
	}
	b, _ := json.Marshal(c)
	os.WriteFile(updateCachePath(dir), b, 0o600)
}

func saveUpdateCache(s *state, latest string, security bool) {
	if s == nil {
		return
	}
	c := loadUpdateCache(s.Dir)
	c.CheckedAt, c.Latest, c.Security = time.Now(), latest, security
	c.save(s.Dir)
}

// noUpdateNotice lists the commands after which no notice is printed.
var noUpdateNotice = map[string]bool{
	"__supervise": true,
	"completion":  true,
	"dev":         true, // local only: no outbound request (#104)
	"init":        true, // local only: no outbound request (#104)
	"mcp":         true,
	"uninstall":   true, // it deletes update-check.json
	"version":     true,
}

// updateCheckOn reports whether this tailworks runs the once-a-day check
// at all: a release build, without CI or TW_NO_UPDATE_CHECK set. It is
// `tw version --json`'s updateCheck.
func updateCheckOn() bool {
	if os.Getenv("CI") != "" || os.Getenv("TW_NO_UPDATE_CHECK") != "" {
		return false
	}
	v := parseSemver(version)
	return v.ok && v.pre == "" // not a development build
}

// stderrIsTerminal reports whether w is a terminal (a var for tests).
var stderrIsTerminal = func(w io.Writer) bool {
	fh, ok := w.(*os.File)
	return ok && term.IsTerminal(int(fh.Fd()))
}

// updateNoticeEnabled reports whether the notice may run after cmd.
func updateNoticeEnabled(cmd string, stderr io.Writer) bool {
	if jsonOut || noUpdateNotice[cmd] || !updateCheckOn() {
		return false
	}
	return stderrIsTerminal(stderr)
}

// updateNotice prints the once-a-day "newer release" line to stderr.
func updateNotice(cmd string, stderr io.Writer) {
	if !updateNoticeEnabled(cmd, stderr) {
		return
	}
	dir, err := stateDirFrom("")
	if err != nil {
		return
	}
	c := loadUpdateCache(dir)
	now := time.Now()
	if now.Sub(c.CheckedAt) > updateEvery {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		rel, err := fetchLatestRelease(ctx)
		cancel()
		c.CheckedAt = now // a failed check waits a day too
		if err == nil {
			c.Latest = strings.TrimPrefix(rel.Tag, "v")
			c.Security = catalog.HasSecurityNotes(rel.Body)
		}
	}
	if msg := updateNoticeLine(c, now); msg != "" {
		fmt.Fprintln(stderr, msg)
		c.NotifiedAt = now
	}
	c.save(dir)
}

// updateNoticeLine is the notice for cache c at now ("" for none).
func updateNoticeLine(c updateCache, now time.Time) string {
	lv, cur := parseSemver(c.Latest), parseSemver(version)
	if !lv.ok || !cur.ok || !lv.newerThan(cur) || now.Sub(c.NotifiedAt) < updateEvery {
		return ""
	}
	return fmt.Sprintf("› tw %s is out (this is %s)%s: %s · details: tw version --check · TW_NO_UPDATE_CHECK=1 turns this notice off", c.Latest, version, securityReleaseTag(c.Security), upgradeCommand(c.Latest))
}
