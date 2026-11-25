package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/catalog"
)

// tw release is the old spelling of tw upgrade releases and tw upgrade
// import (issue #486), kept hidden for one release. releaseImport,
// tarballCatalog and putArtifact are tw upgrade import's (upgrade_import.go).

const releaseUsage = "release ls | release import <tw-<v>-<os>-<arch>.tar.gz>…"

func cmdRelease(args []string) error {
	sub := ""
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			sub, args = normalizeVerb(a), append(append([]string{}, args[:i]...), args[i+1:]...)
			break
		}
	}
	switch sub {
	case "ls":
		upgradeDeprecated(args, "tw release ls", "tw upgrade releases")
		return cmdUpgradeReleases(args)
	case "import":
		upgradeDeprecated(args, "tw release import", "tw upgrade import")
		return cmdUpgradeImport(args)
	case "":
		return fail("bad_request", "tw release is now tw upgrade releases and tw upgrade import", "usage: tw "+upgradeUsage)
	}
	return fail("bad_request", fmt.Sprintf("unknown release subcommand %q (tw release is now tw upgrade releases and tw upgrade import)", sub), "usage: tw "+upgradeUsage)
}

type importResult struct {
	Versions  []string `json:"versions"`
	Artifacts []string `json:"artifacts"`
}

func releaseImport(f *flags, s *state, files []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	res := importResult{Versions: []string{}, Artifacts: []string{}}
	hdr := map[string]string{"Tw-Actor": actor()}
	sumsDone := map[string]bool{}
	for _, file := range files {
		sums, err := os.ReadFile(filepath.Join(filepath.Dir(file), "SHA256SUMS"))
		if err != nil {
			return fail("bad_request", "no SHA256SUMS next to "+file)
		}
		if err := checkSum(file, sums); err != nil {
			return err
		}
		cat, ver, err := tarballCatalog(file)
		if err != nil {
			return err
		}
		if err := s.mgmt().DoHeaders(ctx, "POST", "/v1/releases", map[string]any{"entries": cat}, nil, hdr); err != nil {
			return err
		}
		if !sumsDone[ver] {
			if err := putArtifact(ctx, s, ver, "SHA256SUMS", bytes.NewReader(sums)); err != nil {
				return err
			}
			sumsDone[ver] = true
		}
		fh, err := os.Open(file)
		if err != nil {
			return err
		}
		err = putArtifact(ctx, s, ver, filepath.Base(file), fh)
		fh.Close()
		if err != nil {
			return err
		}
		res.Versions = appendUnique(res.Versions, ver)
		res.Artifacts = append(res.Artifacts, filepath.Base(file))
	}
	emit(f, res, func() {
		fmt.Printf("✓ imported tw %s (%s)\n", strings.Join(res.Versions, ", "), strings.Join(res.Artifacts, ", "))
	})
	return nil
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// tarballCatalog reads share/tailworks/catalog.json and the version from a
// release tarball.
func tarballCatalog(file string) ([]catalog.Entry, string, error) {
	fh, err := os.Open(file)
	if err != nil {
		return nil, "", err
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return nil, "", fail("bad_request", file+": "+err.Error())
	}
	tr := tar.NewReader(zr)
	var cat []catalog.Entry
	ver := ""
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		switch {
		case strings.HasSuffix(hd.Name, "/share/tailworks/catalog.json"):
			b, _ := io.ReadAll(io.LimitReader(tr, 16<<20))
			if cat, err = catalog.Parse(b); err != nil {
				return nil, "", fail("bad_request", file+": catalog.json: "+err.Error())
			}
		case strings.HasSuffix(hd.Name, "/VERSION") && strings.Count(hd.Name, "/") == 1:
			b, _ := io.ReadAll(io.LimitReader(tr, 256))
			ver = strings.TrimSpace(string(b))
		}
	}
	if ver == "" || len(cat) == 0 {
		return nil, "", fail("bad_request", file+" has no VERSION or share/tailworks/catalog.json (a release from before the catalog can't be imported)")
	}
	return cat, ver, nil
}

func putArtifact(ctx context.Context, s *state, ver, name string, body io.Reader) error {
	c := s.mgmt()
	req, err := http.NewRequestWithContext(ctx, "PUT", c.Base+"/v1/releases/"+ver+"/artifacts/"+name, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Tw-Actor", actor())
	resp, err := c.Raw(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fail("bad_request", fmt.Sprintf("upload %s: %s: %s", name, resp.Status, strings.TrimSpace(string(b))))
	}
	return nil
}
