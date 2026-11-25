package imagebuild

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/container"
)

// TestPlanBuildContext: wrangler's image_build_context (and a named
// image's build_context) is the build context, resolved against the app
// dir; it must be a directory. image_vars become the job's build args.
func TestPlanBuildContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, d := range []string{"docker", "py", "shared"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "docker", "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "py", "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "afile"), nil, 0o644)
	classes := []container.Class{{Class: "S", Source: "./docker/Dockerfile", Images: map[string]string{"py": "", "same": ""},
		ImageSources: map[string]string{"py": "./py/Dockerfile", "same": "./py"},
		Build:        &container.Build{Context: ".", Vars: map[string]string{"A": "1"}, ImageContexts: map[string]string{"py": "./shared"}}}}
	jobs, err := Plan(dir, classes)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("jobs %+v", jobs)
	}
	if j := jobs[0]; j.Context != dir || j.Dockerfile != filepath.Join(dir, "docker", "Dockerfile") || j.BuildArgs["A"] != "1" {
		t.Errorf("default image %+v", j)
	}
	if j := jobs[1]; j.Name != "py" || j.Context != filepath.Join(dir, "shared") || j.BuildArgs != nil {
		t.Errorf("py %+v", j)
	}
	if j := jobs[2]; j.Name != "same" || j.Context != filepath.Join(dir, "py") {
		t.Errorf("no build_context: the Dockerfile's dir, got %+v", j)
	}
	for _, bad := range []string{"./missing", "./afile"} {
		c := classes[0]
		c.Build = &container.Build{Context: bad}
		if _, err := Plan(dir, []container.Class{c}); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("context %s: %v", bad, err)
		}
	}
}

// TestBuildArgs: image_vars are passed as sorted --build-arg k=v, and the
// echoed command shows only their names.
func TestBuildArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	t.Parallel()
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	os.WriteFile(df, []byte("FROM scratch\n"), 0o644)
	argv := filepath.Join(dir, "argv")
	docker := filepath.Join(dir, "docker")
	os.WriteFile(docker, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > "+argv+"\n"), 0o755)
	var log strings.Builder
	j := Job{Class: "S", Dockerfile: df, Context: dir, BuildArgs: map[string]string{"TOKEN": "s3cret", "A": "1"}}
	if _, err := Build(context.Background(), j, "arm64", dir, Options{Docker: docker, Log: &log}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(argv)
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	tail := strings.Join(got[len(got)-5:], " ")
	if tail != "--build-arg A=1 --build-arg TOKEN=s3cret "+dir {
		t.Errorf("argv ends %q", tail)
	}
	if strings.Contains(log.String(), "s3cret") || !strings.Contains(log.String(), "--build-arg TOKEN=…") {
		t.Errorf("echoed: %s", log.String())
	}
}
