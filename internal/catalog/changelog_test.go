package catalog

import (
	"os"
	"testing"
)

// TestChangelogUpToDate: CHANGELOG.md (repo root) is Changelog of the
// embedded catalog. Regenerate it with
// `go run ./internal/catalog/changes changelog > CHANGELOG.md`.
func TestChangelogUpToDate(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := Changelog(Embedded()); string(b) != want {
		t.Fatal("CHANGELOG.md is stale: run `go run ./internal/catalog/changes changelog > CHANGELOG.md`")
	}
}
