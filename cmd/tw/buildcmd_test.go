package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// captureBuildOut sends build.command's output to a buffer for the test.
func captureBuildOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := buildCommandOut
	buildCommandOut = &b
	t.Cleanup(func() { buildCommandOut = old })
	return &b
}

func skipNoShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test commands are sh")
	}
}

func TestParseWranglerBuild(t *testing.T) {
	t.Parallel()
	got := parseWranglerBuild(map[string]any{"build": map[string]any{"command": " npm run build ", "cwd": "web", "watch_dir": "web/src"}}, "/p")
	want := wranglerBuild{Command: "npm run build", Cwd: "/p/web", WatchDir: []string{"/p/web/src"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	got = parseWranglerBuild(map[string]any{"build": map[string]any{"command": "x", "watch_dir": []any{"a", "/abs"}}}, "/p")
	if !reflect.DeepEqual(got.WatchDir, []string{"/p/a", "/abs"}) || got.Cwd != "/p" {
		t.Errorf("array watch_dir: %+v", got)
	}
	// wrangler's default watch_dir is ./src.
	if got := parseWranglerBuild(map[string]any{}, "/p"); got.Command != "" || !reflect.DeepEqual(got.WatchDir, []string{"/p/src"}) {
		t.Errorf("no build: %+v", got)
	}
}

// TestBuildRunsBuildCommand: tw deploy runs build.command (in build.cwd,
// with the project's node_modules/.bin on PATH) before it packs, so what
// it writes under assets.directory is deployed; durable-chat's shape.
func TestBuildRunsBuildCommand(t *testing.T) {
	skipNoShell(t)
	out := captureBuildOut(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":       `{"name":"chat","assets":{"directory":"./public"},"build":{"command":"mytool > public/dist/built.txt"}}`,
		"public/index.html":    "<html>",
		"public/dist/.keep":    "",
		"node_modules/.bin/.x": "",
	})
	tool := filepath.Join(dir, "node_modules", ".bin", "mytool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho built by mytool\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range a.Warnings {
		if strings.Contains(w, "build:") {
			t.Errorf("warning for a build the esbuild path runs: %q", w)
		}
	}
	tb, _, err := a.build(nil)
	if err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "› build      mytool > public/dist/built.txt") {
		t.Errorf("output %q doesn't say what ran", out.String())
	}
	files, err := bundle.Unpack(tb)
	if err != nil {
		t.Fatal(err)
	}
	app, err := bundle.Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := app.Assets.Files["/dist/built.txt"]; !ok {
		t.Errorf("the build's output is not in the assets: %v", app.Assets.AssetPaths())
	}
}

func TestBuildCommandFails(t *testing.T) {
	skipNoShell(t)
	out := captureBuildOut(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":    `{"name":"x","assets":{"directory":"./public"},"build":{"command":"echo boom >&2; exit 3"}}`,
		"public/index.html": "<html>",
	})
	a, err := loadApp(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.build(nil)
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "build_command_failed" {
		t.Fatalf("err %v, want build_command_failed", err)
	}
	if !strings.Contains(out.String(), "boom") {
		t.Errorf("the build's output was not streamed: %q", out.String())
	}
}

// TestCustomBuildExclusions: wrangler runs the build itself under
// --wrangler; tw dev runs it through devBuild; Pages projects don't run it.
func TestCustomBuildExclusions(t *testing.T) {
	t.Parallel()
	w := map[string]any{"build": map[string]any{"command": "x"}}
	if b := (&app{Wrangler: w, WranglerFile: "wrangler.jsonc"}).customBuild(); b.Command != "x" {
		t.Errorf("esbuild: %+v", b)
	}
	if b := (&app{Wrangler: w, Opts: appOpts{Wrangler: true}}).customBuild(); b.Command != "" {
		t.Errorf("--wrangler: %+v", b)
	}
	if b := (&app{Wrangler: w, Opts: appOpts{WranglerOut: "out"}}).customBuild(); b.Command != "" {
		t.Errorf("--from-wrangler-out: %+v", b)
	}
	if b := (&app{Wrangler: w, Pages: &pagesProject{}}).customBuild(); b.Command != "" {
		t.Errorf("pages: %+v", b)
	}
	if b := (&app{Wrangler: w, Runtime: bundle.RuntimeNode}).customBuild(); b.Command != "" {
		t.Errorf("node: %+v", b)
	}
	if err := (&app{Wrangler: map[string]any{"build": map[string]any{"command": "exit 1"}}, Opts: appOpts{DevBuild: true}}).prebuild(); err != nil {
		t.Errorf("DevBuild: a.build ran the command: %v", err)
	}
}

// TestDevBuildNoLoop: tw dev reruns build.command only when build.watch_dir
// changes, and its output under assets.directory (outside the watch dir)
// is absorbed into the change snapshot, not taken as an edit. Without
// this, the 150 ms loop rebuilt durable-chat 79 times in 35 s.
func TestDevBuildNoLoop(t *testing.T) {
	skipNoShell(t)
	out := captureBuildOut(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wrangler.jsonc":    `{"name":"x","assets":{"directory":"./public"},"build":{"command":"mkdir -p public/dist && date +%s%N > public/dist/app.js; echo ran"}}`,
		"public/index.html": "<html>",
		"src/client.ts":     "1",
	})
	a, err := loadAppWith(dir, "", appOpts{DevBuild: true})
	if err != nil {
		t.Fatal(err)
	}
	var db devBuild
	if ran, err := db.run(a); !ran || err != nil {
		t.Fatalf("first run: ran %v err %v", ran, err)
	}
	last := snapshot(a.Dir, a.assetsDir())
	// The loop: no edit, so no change and no rebuild.
	for range 3 {
		if now := snapshot(a.Dir, a.assetsDir()); now != last {
			t.Fatal("the build's own output reads as an edit")
		}
		if ran, _ := db.run(a); ran {
			t.Fatal("rebuilt without a watch_dir change")
		}
	}
	// An edit outside watch_dir reloads without a rebuild.
	time.Sleep(10 * time.Millisecond)
	writeFiles(t, dir, map[string]string{"public/index.html": "<html>2"})
	if ran, _ := db.run(a); ran {
		t.Error("an assets edit reran the build")
	}
	// An edit under watch_dir (./src by default) rebuilds once.
	time.Sleep(10 * time.Millisecond)
	writeFiles(t, dir, map[string]string{"src/client.ts": "2"})
	if ran, err := db.run(a); !ran || err != nil {
		t.Fatalf("src edit: ran %v err %v", ran, err)
	}
	if ran, _ := db.run(a); ran {
		t.Error("rebuilt twice for one edit")
	}
	if n := strings.Count(out.String(), "› build"); n != 2 {
		t.Errorf("%d builds, want 2:\n%s", n, out.String())
	}
	// a.build itself doesn't run it under tw dev.
	if _, _, err := a.build(nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "› build"); n != 2 {
		t.Errorf("a.build ran the command under tw dev")
	}
}
