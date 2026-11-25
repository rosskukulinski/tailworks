package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppNameNotes: the workload name's precedence is unchanged (#447),
// but a disagreement between the two files, and a workload tag named after
// another app, each get one note.
func TestAppNameNotes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dir       string
		tw        string // tailworks.jsonc
		wrName    string // wrangler.jsonc name
		flag      string // --name
		env       string // --env
		workload  string
		wantNotes []string // substrings, one per note
	}{
		{name: "wrangler name only", dir: "app", tw: `{"tailnet": {"tags": ["tag:tw-wl-shop"]}}`, wrName: "shop", workload: "shop"},
		{name: "same name in both", dir: "app", tw: `{"name": "shop", "tailnet": {"tags": ["tag:tw-wl-shop"]}}`, wrName: "shop", workload: "shop"},
		{name: "disagree", dir: "app", tw: `{"name": "shop", "tailnet": {"tags": ["tag:tw-wl-shop"]}}`, wrName: "store", workload: "shop",
			wantNotes: []string{`tailworks.jsonc name "shop" wins over wrangler.jsonc name "store", so the workload is "shop"; drop "name" from tailworks.jsonc`}},
		{name: "--name wins, no disagreement note", dir: "app", tw: `{"name": "shop", "tailnet": {"tags": ["tag:tw-wl-shop"]}}`, wrName: "store", flag: "other", workload: "other"},
		{name: "copied and renamed", dir: "app", tw: `{"tailnet": {"tags": ["tag:tw-wl-hello"]}}`, wrName: "shop", workload: "shop",
			wantNotes: []string{`tailnet.tags is tag:tw-wl-hello, named after another app ("hello"): this app ("shop") shares every grant to that tag; for tags of its own, set "tags": ["tag:tw-wl-shop"]`}},
		{name: "both notes", dir: "app", tw: `{"name": "hello", "tailnet": {"tags": ["tag:tw-wl-old"]}}`, wrName: "shop", workload: "hello",
			wantNotes: []string{`tailworks.jsonc name "hello" wins`, `tailnet.tags is tag:tw-wl-old`}},
		{name: "env keeps the base tag", dir: "app", tw: `{"tailnet": {"tags": ["tag:tw-wl-shop"]}}`, wrName: "shop", env: "staging", workload: "shop-staging"},
		{name: "directory name", dir: "shop", tw: `{"tailnet": {"tags": ["tag:tw-wl-shop"]}}`, env: "staging", workload: "shop-staging"},
		{name: "shared tag is not an app tag", dir: "app", tw: `{"tailnet": {"tags": ["tag:tw-app"]}}`, wrName: "shop", workload: "shop"},
		{name: "two tags", dir: "app", tw: `{"tailnet": {"tags": ["tag:tw-wl-hello", "tag:x"]}}`, wrName: "shop", workload: "shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dir)
			w := `{"main": "src/index.js", "compatibility_date": "2026-01-01", "env": {"staging": {}}}`
			if tc.wrName != "" {
				w = `{"name": "` + tc.wrName + `", "main": "src/index.js", "compatibility_date": "2026-01-01", "env": {"staging": {}}}`
			}
			for p, b := range map[string]string{"tailworks.jsonc": tc.tw, "wrangler.jsonc": w, "src/index.js": "export default {}"} {
				os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
				if err := os.WriteFile(filepath.Join(dir, p), []byte(b), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a, err := loadAppWith(dir, tc.flag, appOpts{Env: tc.env})
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != tc.workload {
				t.Errorf("workload %q, want %q", a.Name, tc.workload)
			}
			var notes []string
			for _, s := range a.Warnings {
				if strings.HasPrefix(s, "tailworks.jsonc name ") || strings.HasPrefix(s, "tailnet.tags is ") {
					notes = append(notes, s)
				}
			}
			if len(notes) != len(tc.wantNotes) {
				t.Fatalf("notes %q, want %d", notes, len(tc.wantNotes))
			}
			for i, want := range tc.wantNotes {
				if !strings.Contains(notes[i], want) {
					t.Errorf("note %d = %q, want it to contain %q", i, notes[i], want)
				}
			}
		})
	}
}
