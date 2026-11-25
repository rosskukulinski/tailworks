package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tw upgrade's "latest release" lookup honours TW_RELEASE_API, as tw
// version --check does (issue #528): one variable points both at a mirror.
func TestLatestTagReleaseAPI(t *testing.T) {
	var asked string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Write([]byte(`{"tag_name": "v9.8.7"}`))
	}))
	defer gh.Close()
	t.Setenv("TW_RELEASE_API", gh.URL+"/")
	tag, err := latestTag(context.Background(), "example/mirror")
	if err != nil || tag != "v9.8.7" {
		t.Fatalf("latestTag = %q, %v", tag, err)
	}
	if asked != "/repos/example/mirror/releases/latest" {
		t.Fatalf("asked the mirror for %q", asked)
	}
	t.Setenv("TW_RELEASE_API", "")
	if got := releaseAPI(); got != "https://api.github.com" {
		t.Fatalf("default releaseAPI = %q", got)
	}
}
