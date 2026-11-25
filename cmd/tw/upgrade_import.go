package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
	twversion "github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw upgrade releases and tw upgrade import (issue #486; ADR-041 §8):
// tw-mgmt's release catalog, which a fleet upgrade run plans from and
// hosts that upgrade themselves download their tarball from.
//
//	tw upgrade releases
//	tw upgrade import <tarball>…           (SHA256SUMS next to them)
//	tw upgrade import <version> [--platform <os>-<arch>,…]
//
// With a version, tw downloads the release's SHA256SUMS and SHA256SUMS.sig,
// checks the signature against the pinned release keys (always: there is
// no --allow-unsigned here, and each host verifies the tarball again when
// it installs it), downloads the tarball of each platform the fleet's
// self-upgrading hosts run, checks it against SHA256SUMS and imports it.
// Nothing else fetches a release implicitly: tw upgrade fleet (and --plan)
// plan from what the catalog has.

func cmdUpgradeReleases(args []string) error {
	f := newFlags("upgrade releases")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fail("bad_request", "tw upgrade releases takes no arguments", "usage: tw "+upgradeUsage)
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	return upgradeReleasesList(f, s)
}

func upgradeReleasesList(f *flags, s *state) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cat struct {
		Current string              `json:"current"`
		Items   []mgmt.ReleaseEntry `json:"items"`
	}
	if err := s.mgmt().DoHeaders(ctx, "GET", "/v1/releases", nil, &cat, map[string]string{"Tw-Actor": actor()}); err != nil {
		return err
	}
	emit(f, cat, func() {
		fmt.Printf("tw-mgmt runs %s\n", cat.Current)
		for _, e := range cat.Items {
			extra := ""
			if len(e.ContractMigrations) > 0 {
				extra += " · contract migrations"
			}
			if len(e.Artifacts) > 0 {
				extra += " · " + fmt.Sprint(len(e.Artifacts)) + " artifacts"
			}
			fmt.Printf("  %-10s %s  %s · workerd %s%s\n", e.Version, e.Date, e.Source, e.Workerd, extra)
		}
	})
	return nil
}

func cmdUpgradeImport(args []string) error {
	f := newFlags("upgrade import")
	platforms := f.String("platform", "", "with a version: import the tarballs for these platforms (comma-separated <os>-<arch>; default: the platforms of the hosts that upgrade themselves)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fail("bad_request", "tw upgrade import needs release tarballs or a version", "usage: tw "+upgradeUsage)
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if len(pos) == 1 && !fileExists(pos[0]) {
		if v, err := twversion.Parse(pos[0]); err == nil {
			return upgradeImportVersion(f, s, v.String(), splitComma(*platforms))
		}
	}
	if *platforms != "" {
		return fail("bad_request", "--platform goes with a version (tw upgrade import <version> --platform linux-amd64); tarballs say their own platform")
	}
	return releaseImport(f, s, pos) // release.go
}

// upgradeImportVersion downloads version's tarballs for platforms (default:
// the self-upgrading hosts'), verifies them and imports them.
func upgradeImportVersion(f *flags, s *state, version string, platforms []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for _, p := range platforms {
		if goos, arch, ok := strings.Cut(p, "-"); !ok || goos == "" || arch == "" || strings.ContainsAny(p, "/\\ ") {
			return fail("bad_request", "--platform "+p+": want <os>-<arch> (linux-amd64, linux-arm64, darwin-arm64)")
		}
	}
	if len(platforms) == 0 {
		ps, err := upgradeImportHostPlatforms(ctx, s)
		if err != nil {
			return err
		}
		platforms = ps
		if len(platforms) == 0 {
			// The catalog entry (the notes a plan needs) comes from a
			// tarball, so import one: this machine's.
			platforms = []string{runtime.GOOS + "-" + runtime.GOARCH}
			if !*f.json {
				fmt.Printf("no host upgrades itself: importing the %s tarball for the release notes\n", platforms[0])
			}
		}
	}
	dir, err := os.MkdirTemp("", "tw-upgrade-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	repo := cmpOr(os.Getenv("TW_REPO"), "rosskukulinski/tailworks")
	tag := "v" + version
	base := cmpOr(os.Getenv("TW_RELEASE_BASE"), "https://github.com/"+repo+"/releases/download/"+tag)
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	if err := download(ctx, base+"/SHA256SUMS", sumsPath); err != nil { // upgrade.go
		return err
	}
	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return err
	}
	if err := upgradeImportVerifySums(ctx, base, dir, sums); err != nil {
		return err
	}
	var files []string
	for _, p := range platforms {
		name := "tailworks-" + version + "-" + p + ".tar.gz"
		tgz := filepath.Join(dir, name)
		if err := download(ctx, base+"/"+name, tgz); err != nil {
			return err
		}
		if err := checkSum(tgz, sums); err != nil { // upgrade.go
			return err
		}
		files = append(files, tgz)
	}
	return releaseImport(f, s, files) // release.go
}

// upgradeImportVerifySums checks SHA256SUMS.sig against the pinned release
// keys (TW_RELEASE_SIGNERS replaces them, as for tw upgrade). There is no
// way past it: a fleet's hosts install what is imported here.
func upgradeImportVerifySums(ctx context.Context, base, dir string, sums []byte) error {
	sigPath := filepath.Join(dir, "SHA256SUMS.sig")
	if err := download(ctx, base+"/SHA256SUMS.sig", sigPath); err != nil {
		return fail("release_unsigned", "refusing an unsigned release: "+base+" has no SHA256SUMS.sig").
			withFix("import the tarballs of a build you made yourself: tw upgrade import <tarball>…")
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	keys, err := releaseSigners() // upgradesig.go
	if err == nil {
		err = releasesig.Verify(keys, sums, sig)
	}
	if err != nil {
		e := fail("release_bad_signature", "refusing a release whose SHA256SUMS.sig doesn't verify: "+err.Error())
		if errors.Is(err, releasesig.ErrNoKeys) {
			e.Lines = append(e.Lines, "this tw build pins no release signing key, so it can't verify any release")
		}
		return e.withFix("check the version and TW_RELEASE_BASE / TW_REPO, then try again")
	}
	return nil
}

// upgradeImportHostPlatforms is the <os>-<arch> of every registered host
// that upgrades itself (tw-hostd -self-upgrade), sorted.
func upgradeImportHostPlatforms(ctx context.Context, s *state) ([]string, error) {
	type hostPlatform struct {
		OS       string   `json:"os"`
		Arch     string   `json:"arch"`
		Features []string `json:"features"`
	}
	hosts, err := listAll[hostPlatform](ctx, s.mgmt(), "/v1/hosts", 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range hosts {
		if h.OS == "" || h.Arch == "" || !slices.Contains(h.Features, "self-upgrade") {
			continue
		}
		if p := h.OS + "-" + h.Arch; !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}
