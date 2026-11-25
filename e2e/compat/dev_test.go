//go:build e2ecompat

// E2E-COMPAT under `tw dev` (`make e2e-compat-dev`): the upstream examples
// of issue #325 (the projects with a Section), each run by `tw dev` (the
// same tw-hostd, netd and workerd as a deployment, with the embedded
// control plane: no tailnet, no credentials) and probed on 127.0.0.1 with
// the same probe the deployed run uses. This is the half of the suite the
// GitHub nightly can run.
package compat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// devSelected is selected(), less what `tw dev` can't run the way the
// deployed run does: without TW_COMPAT_ONLY only the published upstream
// examples (Section), and never a project with post-deploy tw commands
// other than D1 migrations (devRunsSetup).
// devRunsSetup: tw dev can run p the way the deployed run does. True with
// no post-deploy steps, or when they are all `d1 migrations apply`, which
// tw dev does itself on every start (docs/d1.md#tw-dev).
func devRunsSetup(p *Project) bool {
	for _, s := range p.Setup {
		if len(s) < 3 || s[0] != "d1" || s[1] != "migrations" || s[2] != "apply" {
			return false
		}
	}
	return true
}

func devSelected(t *testing.T) []*Project {
	var out []*Project
	for _, p := range selected(t) {
		if os.Getenv("TW_COMPAT_ONLY") == "" && p.Section == "" {
			continue
		}
		if !devRunsSetup(p) {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		t.Fatal("no project to run under tw dev")
	}
	return out
}

func TestCompatDev(t *testing.T) {
	root, _ := nettest.FindRepoRoot()
	if err := Validate(Projects(), compatAnchors(t, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TW_WORKERD", wd)
	ps := devSelected(t)
	t0 := time.Now()
	prs := prepareAll(ps, CompatTag, 4)
	t.Logf("prepared %d projects in %v (cache %s)", len(ps), time.Since(t0).Round(time.Second), CacheDir())
	bin := cliflow.BuildBinaries(t)
	work, err := os.MkdirTemp("/tmp", "tw-compat-dev-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })

	var (
		mu      sync.Mutex
		results []Result
	)
	sem := make(chan struct{}, 3)
	t.Run("projects", func(t *testing.T) {
		for _, p := range ps {
			pr := prs[p.ID]
			t.Run(p.ID, func(t *testing.T) {
				t.Parallel()
				sem <- struct{}{}
				defer func() { <-sem }()
				r := runDev(pr, filepath.Join(bin, "tw"), filepath.Join(work, p.ID))
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
				switch r.Outcome {
				case "PASS", "XFAIL":
					t.Logf("%s: %s", r.Outcome, r.Detail)
				case "XPASS":
					t.Errorf("XPASS: %s is expected to fail (COMPAT.md #%s) but passed: update COMPAT.md and the project", p.ID, p.ExpectIn("dev").Compat)
				default:
					t.Errorf("FAIL: %s", r.Detail)
				}
			})
		}
	})
	path := filepath.Join(root, "e2e", "real", "results", "compat-dev-"+time.Now().Format("20060102-150405")+".json")
	writeRunFile(t, path, "dev", root, nil, results)
}

// devProc is a running `tw dev --json`.
type devProc struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  []string // stdout lines (NDJSON events)
	errb bytes.Buffer
	done chan struct{}
}

func (d *devProc) lines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.out...)
}

// waitLine waits for a stdout line ok accepts.
func (d *devProc) waitLine(timeout time.Duration, ok func(string) bool) (string, error) {
	dl := time.Now().Add(timeout)
	for {
		for _, l := range d.lines() {
			if ok(l) {
				return l, nil
			}
		}
		select {
		case <-d.done:
			return "", fmt.Errorf("tw dev exited: %s", tail(d.errb.String()+strings.Join(d.lines(), "\n"), 3000))
		default:
		}
		if time.Now().After(dl) {
			return "", fmt.Errorf("not within %v: %s", timeout, tail(d.errb.String()+strings.Join(d.lines(), "\n"), 3000))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (d *devProc) stop() {
	d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(20 * time.Second):
		d.cmd.Process.Kill()
		<-d.done
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func runDev(pr *Prepared, tw, state string) Result {
	p := pr.P
	r := Result{ID: p.ID, Source: fmt.Sprintf("%s/%s@%s", p.Repo, p.Dir, p.Commit[:12]), Exercises: p.Exercises,
		PrepareMS: pr.Dur.Milliseconds(), Workarounds: p.Workarounds, Changes: p.Changes}
	fail := func(f string, a ...any) Result {
		r.Outcome, r.Detail = "FAIL", fmt.Sprintf(f, a...)
		return r
	}
	if pr.Err != nil {
		return fail("prepare: %v", pr.Err)
	}
	// The README's `wrangler secret put` is .dev.vars in dev (as for
	// wrangler dev). Removed afterwards: the deployed run shares the copy.
	if len(p.Secrets) > 0 {
		var b strings.Builder
		for _, k := range sortedKeys(p.Secrets) {
			fmt.Fprintf(&b, "%s=%q\n", k, p.Secrets[k])
		}
		dv := filepath.Join(pr.DeployDir, ".dev.vars")
		if err := os.WriteFile(dv, []byte(b.String()), 0o600); err != nil {
			return fail("%v", err)
		}
		defer os.Remove(dv)
	}
	// The port is chosen here (an OriginVar needs it before the start), so
	// something else can take it first: try again with another one.
	var (
		d    *devProc
		base string
		t0   time.Time
		err  error
	)
	for attempt := 1; ; attempt++ {
		d, base, t0, err = startDev(pr, tw, state)
		if d == nil {
			return fail("tw dev: %v", err)
		}
		if err == nil || attempt == 3 || !strings.Contains(err.Error(), "address already in use") {
			break
		}
		d.stop()
	}
	defer d.stop()
	if p.ExpectIn("dev").Kind == ExpectRefused {
		// It must not start, and the error must name the surface.
		if err == nil {
			r.Outcome, r.Detail = "XPASS", "tw dev started it"
			return r
		}
		msg := errorLines(strings.Join(d.lines(), "\n") + "\n" + d.errb.String())
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(p.ExpectIn("dev").Error)) {
			return fail("tw dev didn't start it, but its error doesn't mention %q: %v", p.ExpectIn("dev").Error, err)
		}
		r.Outcome, r.Detail = "XFAIL", "refused as expected: "+tail(msg, 400)
		return r
	}
	if err != nil {
		return fail("tw dev: %v", err)
	}
	r.DeployMS = time.Since(t0).Milliseconds()
	for _, l := range strings.Split(d.errb.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "›") || strings.Contains(l, "warning") || strings.Contains(l, "ignored") {
			r.Warnings = append(r.Warnings, strings.TrimSpace(l))
		}
	}
	run := &Run{P: p, Base: base, HC: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{}},
		Tw: func(args ...string) (string, string, error) {
			return "", "", fmt.Errorf("tw %s: not available under tw dev", strings.Join(args, " "))
		},
		WaitLogFn: func(s string, timeout time.Duration) (string, error) {
			return d.waitLine(timeout, func(l string) bool { return strings.Contains(l, s) })
		}}
	t1 := time.Now()
	perr := p.Probe(run)
	r.ProbeMS, r.Steps = time.Since(t1).Milliseconds(), run.Steps
	switch {
	case p.ExpectIn("dev").Kind == ExpectProbeFail && perr == nil:
		r.Outcome, r.Detail = "XPASS", "probe passed"
	case p.ExpectIn("dev").Kind == ExpectProbeFail:
		r.Outcome, r.Detail = "XFAIL", "failed as expected ("+p.ExpectIn("dev").Why+"): "+tail(perr.Error(), 300)
	case perr != nil:
		ls := d.lines()
		r.Steps = append(r.Steps, "tw dev output (tail): "+tail(strings.Join(ls[max(0, len(ls)-40):], "\n"), 3000))
		return fail("probe: %v", perr)
	default:
		r.Outcome, r.Detail = "PASS", fmt.Sprintf("%d steps", len(run.Steps))
	}
	return r
}

// startDev starts `tw dev --json` for a prepared project on a free port
// and waits for its ready event. d is nil only when it could not be
// started at all; err is why it didn't get ready.
func startDev(pr *Prepared, tw, state string) (d *devProc, base string, t0 time.Time, err error) {
	p := pr.P
	port, err := freePort()
	if err != nil {
		return nil, "", t0, err
	}
	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	if p.OriginVar != "" {
		if err := setOriginVar(p, pr.Root, p.OriginVar, base+"/mcp"); err != nil {
			return nil, "", t0, err
		}
	}
	args := append([]string{"dev", pr.DeployDir, "--json", "--port", fmt.Sprint(port), "--console-port", "-1", "--state-dir", state}, p.DeployArgs...)
	d = &devProc{cmd: exec.Command(tw, args...), done: make(chan struct{})}
	d.cmd.Dir = filepath.Dir(state)
	d.cmd.Stderr = &d.errb
	stdout, err := d.cmd.StdoutPipe()
	if err != nil {
		return nil, "", t0, err
	}
	t0 = time.Now()
	if err := d.cmd.Start(); err != nil {
		return nil, "", t0, err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 4<<20)
		for sc.Scan() {
			d.mu.Lock()
			d.out = append(d.out, sc.Text())
			d.mu.Unlock()
		}
		d.cmd.Wait()
		close(d.done)
	}()
	_, err = d.waitLine(4*time.Minute, func(l string) bool {
		var ev struct{ Event string }
		return json.Unmarshal([]byte(l), &ev) == nil && ev.Event == "ready"
	})
	return d, base, t0, err
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// --- OriginVar ---

// setOriginVar sets the wrangler var name to value in the project's
// wrangler.jsonc and in the config `vite build` generated from it (the one
// .wrangler/deploy/config.json points at), which is what gets deployed.
func setOriginVar(p *Project, root, name, value string) error {
	files := []string{filepath.Join(root, "wrangler.jsonc")}
	if dd, err := (&Project{DeployDir: "redirect"}).deployDir(root); err == nil {
		files = append(files, filepath.Join(dd, "wrangler.json"))
	}
	re := regexp.MustCompile(`("` + regexp.QuoteMeta(name) + `"\s*:\s*)"[^"]*"`)
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || !re.Match(b) {
			continue
		}
		v, _ := json.Marshal(value)
		if err := os.WriteFile(f, re.ReplaceAll(b, append([]byte("${1}"), v...)), 0o644); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("%s: no wrangler config under %s has a %q var", p.ID, root, name)
	}
	return nil
}

// resetOriginVar puts the var back to the value the example ships with
// (read from the pinned checkout): the prepared copy is reused between
// runs, and the deployed run's first deploy must be the example as it is.
func resetOriginVar(p *Project, root string) error {
	src, err := checkout(p.Repo, p.Commit)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(p.Dir), "wrangler.jsonc"))
	if err != nil {
		return err
	}
	m := regexp.MustCompile(`"` + regexp.QuoteMeta(p.OriginVar) + `"\s*:\s*"([^"]*)"`).FindSubmatch(b)
	if m == nil {
		return fmt.Errorf("%s: the example's wrangler.jsonc has no %q var", p.ID, p.OriginVar)
	}
	return setOriginVar(p, root, p.OriginVar, string(m[1]))
}
