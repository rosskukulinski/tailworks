// Package pyodide pins the Pyodide bundles the pinned workerd runs Python
// Workers with, and fetches them into a host's cache directory
// (ADR-055, docs/python.md).
//
// workerd downloads a Pyodide bundle (pyodide_<version>_<date>_<n>.capnp.bin)
// the first time it starts a Python Worker, unless the bundle is already in
// its --pyodide-bundle-disk-cache-dir. Sandboxed workerd has no network and
// may not write outside its instance, so hostd fetches every pinned bundle
// once per host, checks its SHA-256, and gives workerd the directory
// read-only. An offline host is seeded by copying the files there.
//
// Which bundle a Worker needs follows its compatibility date and flags
// (python_workers_20250116, python_workers_314, …), so every bundle the
// pinned workerd may ask for is pinned. Update Bundles with the workerd pin
// (TestBundlesMatchWorkerd checks each pin's version and date are ones the
// pinned binary knows; docs/python.md#updating-the-pins).
package pyodide

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BaseURL is where workerd itself downloads bundles from.
const BaseURL = "https://pyodide-capnp-bin.edgeworker.net/"

// Bundle is one pinned Pyodide bundle.
type Bundle struct {
	File   string // file name, as workerd looks it up in its cache dir
	SHA256 string // hex
	Size   int64
}

//go:embed bundles.txt
var bundlesTxt string

// Bundles are the bundles the pinned workerd asks for (bundles.txt, which
// guest/build-image.sh reads too), oldest first.
var Bundles = parseBundles(bundlesTxt)

// parseBundles reads bundles.txt: "<file> <sha256> <size>" per line, # comments.
func parseBundles(s string) []Bundle {
	var out []Bundle
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if len(f) != 3 {
			panic("pyodide: bad bundles.txt line: " + l)
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			panic("pyodide: bad bundles.txt size: " + l)
		}
		out = append(out, Bundle{File: f[0], SHA256: f[1], Size: n})
	}
	return out
}

// GuestDir is where the microVM guest image carries the bundles
// (guest/build-image.sh), read-only.
const GuestDir = "/usr/share/tailworks/pyodide"

// Missing returns the pinned bundles not in dir (by name and size; Ensure
// checks the digest when it writes one).
func Missing(dir string) []Bundle {
	var out []Bundle
	for _, b := range Bundles {
		if st, err := os.Stat(filepath.Join(dir, b.File)); err != nil || st.Size() != b.Size {
			out = append(out, b)
		}
	}
	return out
}

var mu sync.Mutex // one fetch at a time per process

// Ensure makes dir hold every pinned bundle, downloading the missing ones
// from baseURL ("" = BaseURL) and checking each SHA-256 before it is
// renamed into place. The directory and files end up read-only for
// everyone but their owner.
func Ensure(ctx context.Context, dir, baseURL string) error {
	mu.Lock()
	defer mu.Unlock()
	miss := Missing(dir)
	if len(miss) == 0 {
		return nil
	}
	if baseURL == "" {
		baseURL = BaseURL
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, b := range miss {
		if err := fetch(ctx, dir, baseURL, b); err != nil {
			return fmt.Errorf("pyodide: %s: %w (an offline host: copy the files listed in docs/python.md#offline-hosts into %s)", b.File, err, dir)
		}
	}
	return nil
}

func fetch(ctx context.Context, dir, baseURL string, b Bundle) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+b.File, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", req.URL, resp.Status)
	}
	f, err := os.CreateTemp(dir, ".fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, b.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != b.Size {
		return fmt.Errorf("got %d bytes, want %d", n, b.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
		return errors.New("SHA-256 " + got + " does not match the pin " + b.SHA256)
	}
	if err := os.Chmod(f.Name(), 0o444); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, b.File))
}

// UserDir is the per-user cache tw dev (and tests) share, so a laptop
// fetches the bundles once: <user cache dir>/tailworks/pyodide.
func UserDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "tailworks", "pyodide"), nil
}

// WorkerdArgs are the workerd flags that point it at dir.
func WorkerdArgs(dir string) []string {
	return []string{"--pyodide-bundle-disk-cache-dir", dir}
}
