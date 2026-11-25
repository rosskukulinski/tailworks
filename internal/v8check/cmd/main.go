// Command v8check (make v8-check; scripts/v8-check.sh) compares the V8 in
// the pinned workerd with the V8 Chrome's stable channel runs (issue #338;
// docs/RELEASING.md "Upstream advisories").
//
//	go run ./internal/v8check/cmd [-workerd PATH] [-chrome URL|FILE] [-issue-body FILE] [-json] [-check-pin=false]
//
// It reads the V8 version out of the workerd binary (-workerd, default
// node_modules/.bin/workerd; the binary must be the pinned release:
// `workerd --version` is checked against internal/version.WorkerdPin,
// unless -check-pin=false, which lets another platform's binary be scanned),
// fetches Chrome stable's version (-chrome: chromiumdash's fetch_releases,
// or a file with the same JSON for tests), prints one line and exits 0
// when the pin's V8 milestone is at or past Chrome stable's, 10 when it is
// behind, 1 on an error. -issue-body writes the issue text for the behind
// case (scripts/v8-check.sh files it); -json prints the Result instead of
// the line.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/v8check"
	"github.com/rosskukulinski/tailworks/internal/version"
)

func main() {
	rc, err := run(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "v8check:", err)
		os.Exit(1)
	}
	os.Exit(rc)
}

func run(args []string, out io.Writer) (int, error) {
	fs := flag.NewFlagSet("v8check", flag.ContinueOnError)
	workerd := fs.String("workerd", "node_modules/.bin/workerd", "the pinned workerd binary")
	chrome := fs.String("chrome", v8check.ChromiumDashURL, "Chrome stable's release: chromiumdash's fetch_releases URL, or a file with its JSON")
	issueBody := fs.String("issue-body", "", "write the issue body for the behind case to this file")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	runURL := fs.String("run-url", os.Getenv("RUN_URL"), "the workflow run, for the issue body")
	checkPin := fs.Bool("check-pin", true, "run `workerd --version` and require the pinned release (false to scan another platform's binary)")
	if err := fs.Parse(args); err != nil {
		return 1, err
	}

	bin, err := resolveWorkerd(*workerd)
	if err != nil {
		return 1, err
	}
	if *checkPin {
		if err := checkPinned(bin); err != nil {
			return 1, err
		}
	}
	f, err := os.Open(bin)
	if err != nil {
		return 1, err
	}
	v8, err := v8check.WorkerdV8(f)
	f.Close()
	if err != nil {
		return 1, fmt.Errorf("%s: %v", bin, err)
	}

	raw, err := readChrome(*chrome)
	if err != nil {
		return 1, err
	}
	rel, err := v8check.ParseChromiumDash(raw)
	if err != nil {
		return 1, err
	}
	res, err := v8check.Compare(version.WorkerdPin, v8, rel)
	if err != nil {
		return 1, err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(out, string(b))
	} else {
		fmt.Fprintln(out, res.Summary())
	}
	if *issueBody != "" {
		if err := os.WriteFile(*issueBody, []byte(res.IssueBody(*runURL)), 0o644); err != nil {
			return 1, err
		}
	}
	if res.Behind {
		return 10, nil
	}
	return 0, nil
}

// resolveWorkerd follows symlinks and, when the path is npm's shim rather
// than the binary (a small file), looks for the platform package's binary
// next to it (node_modules/@cloudflare/workerd-<os>-<arch>/bin/workerd).
func resolveWorkerd(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%s: %v (run `make deps`, or set -workerd)", p, err)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if st.Size() > 1<<24 { // 16 MiB: the binary, not a shim
		return real, nil
	}
	nm := real
	for nm != "/" && nm != "." && filepath.Base(nm) != "node_modules" {
		nm = filepath.Dir(nm)
	}
	cands, _ := filepath.Glob(filepath.Join(nm, "@cloudflare", "workerd-*", "bin", "workerd*"))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Size() > 1<<24 {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s is not the workerd binary (%d bytes) and no @cloudflare/workerd-* package is installed", real, st.Size())
}

// checkPinned runs `workerd --version` and compares its date with the pin's:
// a stale node_modules would measure the wrong release.
func checkPinned(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --version: %v: %s", bin, err, strings.TrimSpace(string(out)))
	}
	v := strings.TrimSpace(string(out))
	if !strings.Contains(v, version.WorkerdPinDate()) {
		return fmt.Errorf("%s is %q but the pin is %s (%s): run `npm ci`", bin, v, version.WorkerdPin, version.WorkerdPinDate())
	}
	return nil
}

func readChrome(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tailworks-v8check/"+version.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching Chrome stable's version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", src, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
