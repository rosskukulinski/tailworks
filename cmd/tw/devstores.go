package main

// tw dev keeps the app's Workers KV namespaces between runs, as
// `wrangler dev` keeps its local state: the in-memory mgmt's KV journals
// are saved to <app>/.tailworks/dev-stores.json when tw dev stops and
// loaded when it starts; each store object rebuilds itself from its journal.
// The directory ignores itself in git (.tailworks/.gitignore), and the file is
// readable by the owner only (it holds the data, unencrypted).

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/internal/kvwire"
)

func devStoresFile(appDir string) string {
	return filepath.Join(appDir, ".tailworks", "dev-stores.json")
}

// loadDevStores imports the saved stores into tenantID (none saved: none).
func loadDevStores(fm *fakemgmt.Server, tenantID, appDir string) (int, error) {
	b, err := os.ReadFile(devStoresFile(appDir))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var snaps []fakemgmt.StoreSnapshot
	if err := json.Unmarshal(b, &snaps); err != nil {
		return 0, fail("bad_request", "can't read "+devStoresFile(appDir)+": "+err.Error()).withFix("rm " + devStoresFile(appDir) + "    (starts the dev stores empty)")
	}
	fm.ImportStores(tenantID, snaps)
	return len(snaps), nil
}

// saveDevStores saves tenantID's stores (nothing, and no file, when there
// are none and none were saved before).
//
// Only KV: a kind whose journal points at data kept elsewhere (R2's index
// and its blobs in the dev process's temporary directory) would come back
// dangling.
func saveDevStores(fm *fakemgmt.Server, tenantID, appDir string) error {
	var snaps []fakemgmt.StoreSnapshot
	for _, sn := range fm.ExportStores(tenantID) {
		if sn.Kind == kvwire.Kind {
			snaps = append(snaps, sn)
		}
	}
	p := devStoresFile(appDir)
	if _, err := os.Stat(p); len(snaps) == 0 && os.IsNotExist(err) {
		return nil
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); os.IsNotExist(err) {
		os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# tw dev local state\n*\n"), 0o644)
	}
	b, _ := json.Marshal(snaps)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
