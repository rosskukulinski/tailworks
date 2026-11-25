package compat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CacheDir is where upstream checkouts and prepared projects live:
// $TW_COMPAT_CACHE, else ~/.cache/tailworks-compat. Nothing from upstream
// is vendored into this repo; each run fetches the pinned commit once and
// reuses it.
func CacheDir() string {
	if d := os.Getenv("TW_COMPAT_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "tailworks-compat")
}

// Prepared is a project ready to deploy.
type Prepared struct {
	P *Project
	// Root is the prepared copy (overlays applied, dependencies installed,
	// built).
	Root string
	// DeployDir is the directory `tw deploy` is given (Root, or what the
	// project's DeployDir resolves to after the build).
	DeployDir string
	// Log is the prepare output (npm, the build).
	Log string
	// Dur is how long preparing took (0 when it came from the cache).
	Dur time.Duration
	// Err is why preparing failed.
	Err error
}

// checkout returns a worktree of repo at commit with only dirs checked out
// (a blobless partial clone shared by every project from that repo).
func checkout(repo, commit string) (string, error) {
	base := filepath.Join(CacheDir(), "src", strings.ReplaceAll(repo, "/", "__")+"@"+commit[:12])
	if _, err := os.Stat(filepath.Join(base, ".tw-compat-ok")); err == nil {
		return base, nil
	}
	os.RemoveAll(base)
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return "", err
	}
	tmp := base + ".tmp"
	os.RemoveAll(tmp)
	url := "https://github.com/" + repo + ".git"
	steps := [][]string{
		{"git", "init", "-q", tmp},
		{"git", "-C", tmp, "remote", "add", "origin", url},
		{"git", "-C", tmp, "fetch", "-q", "--depth", "1", "--filter=blob:none", "origin", commit},
		{"git", "-C", tmp, "-c", "advice.detachedHead=false", "checkout", "-q", "FETCH_HEAD"},
	}
	for _, s := range steps {
		if out, err := run("", nil, s...); err != nil {
			return "", fmt.Errorf("%s: %v\n%s", strings.Join(s, " "), err, out)
		}
	}
	if out, err := run(tmp, nil, "git", "rev-parse", "HEAD"); err != nil || strings.TrimSpace(out) != commit {
		return "", fmt.Errorf("%s: checked out %q, want %s (%v)", repo, strings.TrimSpace(out), commit, err)
	}
	if err := os.Rename(tmp, base); err != nil {
		return "", err
	}
	return base, os.WriteFile(filepath.Join(base, ".tw-compat-ok"), nil, 0o644)
}

// key identifies a project's prepared copy: everything that changes it.
func (p *Project) key() string {
	b, _ := json.Marshal([]any{p.Repo, p.Commit, p.Dir, p.C3, p.Pins, p.AddDeps, p.NpmArgs, p.Build, p.Install, p.Files, prepareVersion})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:6])
}

// prepareVersion: bump when Prepare changes what it produces.
const prepareVersion = 1

// Prepare fetches, copies, overlays, installs and builds p (cached by
// p.key()). The tailworks.jsonc overlay is (re)written on every call: it
// carries the run's tag.
func Prepare(p *Project, tag string) (*Prepared, error) {
	t0 := time.Now()
	root := filepath.Join(CacheDir(), "prepared", p.ID+"-"+p.key())
	pr := &Prepared{P: p, Root: root}
	logPath := filepath.Join(root, ".tw-compat-prepare.log")
	if _, err := os.Stat(filepath.Join(root, ".tw-compat-ok")); err != nil {
		os.RemoveAll(root)
		log, err := prepare(p, root)
		if err != nil {
			return pr, fmt.Errorf("%w\n%s", err, tail(log, 6000))
		}
		os.WriteFile(logPath, []byte(log), 0o644)
		os.WriteFile(filepath.Join(root, ".tw-compat-ok"), nil, 0o644)
		pr.Dur = time.Since(t0)
	}
	lb, _ := os.ReadFile(logPath)
	pr.Log = string(lb)
	dd, err := p.deployDir(root)
	if err != nil {
		return pr, err
	}
	pr.DeployDir = dd
	return pr, writeOverlay(p, dd, tag)
}

func prepare(p *Project, root string) (string, error) {
	var log bytes.Buffer
	src, err := checkout(p.Repo, p.Commit)
	if err != nil {
		return "", err
	}
	from := filepath.Join(src, filepath.FromSlash(p.Dir))
	if err := copyTree(from, root); err != nil {
		return "", fmt.Errorf("copy %s: %w", p.Dir, err)
	}
	if p.C3 {
		// What create-cloudflare does to a template: name the Worker and
		// the package, and date it.
		if err := c3Fill(root, p.ID); err != nil {
			return "", err
		}
	}
	for name, body := range p.Files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	if len(p.Pins) > 0 || len(p.AddDeps) > 0 {
		if err := pinDeps(filepath.Join(root, "package.json"), p.Pins, p.AddDeps); err != nil {
			return "", err
		}
	}
	if p.Install != InstallNone {
		if _, err := os.Stat(filepath.Join(root, "package.json")); err == nil {
			// --ignore-scripts: upstream code is fetched, not reviewed. Every
			// native tool these projects use (esbuild, rollup, workerd,
			// lightningcss) ships as an optional per-platform package.
			args := []string{"npm", "install", "--no-audit", "--no-fund", "--ignore-scripts"}
			if _, err := os.Stat(filepath.Join(root, "package-lock.json")); err == nil && p.Install != InstallUpgrade {
				args[1] = "ci"
			}
			args = append(args, p.NpmArgs...)
			out, err := run(root, nil, args...)
			fmt.Fprintf(&log, "$ %s\n%s\n", strings.Join(args, " "), out)
			if err != nil {
				return log.String(), fmt.Errorf("%s: %w", strings.Join(args, " "), err)
			}
		}
	}
	for _, b := range p.Build {
		out, err := run(root, nil, b...)
		fmt.Fprintf(&log, "$ %s\n%s\n", strings.Join(b, " "), out)
		if err != nil {
			return log.String(), fmt.Errorf("%s: %w", strings.Join(b, " "), err)
		}
	}
	return log.String(), nil
}

// deployDir resolves p.DeployDir in root. "redirect" follows the Vite
// plugin's redirected config (.wrangler/deploy/config.json → the
// generated wrangler.json's directory), which wrangler deploy follows and
// tw deploy does not (yet).
func (p *Project) deployDir(root string) (string, error) {
	switch p.DeployDir {
	case "":
		return root, nil
	case "redirect":
		b, err := os.ReadFile(filepath.Join(root, ".wrangler", "deploy", "config.json"))
		if err != nil {
			return "", fmt.Errorf("no redirected config after the build: %w", err)
		}
		var r struct {
			ConfigPath string `json:"configPath"`
		}
		if err := json.Unmarshal(b, &r); err != nil || r.ConfigPath == "" {
			return "", fmt.Errorf(".wrangler/deploy/config.json: %v %s", err, b)
		}
		return filepath.Dir(filepath.Join(root, ".wrangler", "deploy", filepath.FromSlash(r.ConfigPath))), nil
	default:
		return filepath.Join(root, filepath.FromSlash(p.DeployDir)), nil
	}
}

// writeOverlay writes the only tailworks-specific file a project gets: a
// tailworks.jsonc with the workload's name and the run's tag. Without it the
// workload would be tagged tag:tw-wl-<workload> (wrangler.md, "Without a
// tailworks.jsonc"), one new tagOwner per project; with it every project
// shares one tag. Durable Object classes get no node of their own, as
// without one (they are missing from tailnet.objects: docs/egress-modes.md).
func writeOverlay(p *Project, dir, tag string) error {
	if p.Expect.Kind == ExpectRefused && p.NoOverlay {
		return nil
	}
	tn := map[string]any{"tags": []string{tag}}
	if p.TailnetHTTPS {
		tn["https"] = true
	}
	o := map[string]any{"name": p.ID, "tailnet": tn}
	b, _ := json.MarshalIndent(o, "", "  ")
	hdr := "// Written by e2e/compat (the compatibility suite): the workload's name and tag.\n"
	return os.WriteFile(filepath.Join(dir, "tailworks.jsonc"), append([]byte(hdr), b...), 0o644)
}

// c3Fill replaces create-cloudflare's placeholders in the template copy.
func c3Fill(root, name string) error {
	date := "2025-10-08" // the compatibility_date cloudflare/templates pins at the same time
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".json", ".jsonc", ".toml", ".ts", ".js", ".py", ".md":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		nb := bytes.ReplaceAll(b, []byte("<WORKER_NAME>"), []byte(name))
		nb = bytes.ReplaceAll(nb, []byte("<PACKAGE_NAME>"), []byte(name))
		nb = bytes.ReplaceAll(nb, []byte("<COMPATIBILITY_DATE>"), []byte(date))
		if bytes.Equal(b, nb) {
			return nil
		}
		return os.WriteFile(path, nb, 0o644)
	})
}

// pinDeps sets dependency versions in package.json (a monorepo example's
// "workspace:*" or "*" becomes a published version) and adds the
// dependencies in add (what the monorepo hoists for it).
func pinDeps(path string, pins, add map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		return err
	}
	for name, v := range pins {
		set := false
		for _, sec := range []string{"dependencies", "devDependencies"} {
			if m, ok := pkg[sec].(map[string]any); ok {
				if _, ok := m[name]; ok {
					m[name] = v
					set = true
				}
			}
		}
		if !set {
			return fmt.Errorf("pin %s: not a dependency in %s", name, path)
		}
	}
	if len(add) > 0 {
		m, _ := pkg["dependencies"].(map[string]any)
		if m == nil {
			m = map[string]any{}
			pkg["dependencies"] = m
		}
		for name, v := range add {
			if _, ok := m[name]; ok {
				return fmt.Errorf("add %s: already a dependency in %s", name, path)
			}
			m[name] = v
		}
	}
	nb, _ := json.MarshalIndent(pkg, "", "  ")
	return os.WriteFile(path, nb, 0o644)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(dst, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// run runs a command; the environment is this process's minus anything
// that names a Tailscale credential, plus env.
func run(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TS_") || strings.HasPrefix(k, "TW_E2E_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "CI=1", "NO_COLOR=1", "WRANGLER_SEND_METRICS=false")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
