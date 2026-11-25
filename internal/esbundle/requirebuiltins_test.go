package esbundle

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeRequireFixture writes an app whose CommonJS dependencies require()
// Node builtins, the way dotenv does (issue #356).
func writeRequireFixture(t *testing.T, flags string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"wrangler.jsonc": `{ "name": "req", "main": "index.js", "compatibility_date": "2026-06-11", "compatibility_flags": [` + flags + `] }`,
		"index.js": `import "fakedotenv/config";
import { hash } from "@scope/hasher";
export default { fetch() { return new Response(process.env.FAKE + " " + hash()); } };
`,
		"node_modules/fakedotenv/package.json": `{ "name": "fakedotenv", "main": "lib/main.js" }`,
		"node_modules/fakedotenv/config.js":    `(function () { require("./lib/main").config(); })();`,
		"node_modules/fakedotenv/lib/main.js": `const fs = require('fs')
const path = require('path')
const os = require('node:os')
const { EventEmitter } = require('events')
function config () {
  process.env.FAKE = [typeof fs.readFileSync, path.join('a', 'b'), typeof os.EOL, typeof EventEmitter].join(',')
}
module.exports.config = config
`,
		"node_modules/@scope/hasher/package.json": `{ "name": "@scope/hasher", "main": "index.js" }`,
		"node_modules/@scope/hasher/index.js":     `exports.hash = function () { return typeof require('crypto').createHash }`,
	}
	for name, s := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRequireBuiltins: with nodejs_compat, a require() of a Node builtin in
// a CommonJS dependency imports node:* (no __require shim, which throws in
// workerd); without it, each require is a stub that throws, when it runs,
// an error naming the package and the flag. build.mjs bundles the same.
func TestRequireBuiltins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		flags string
		want  []string
		not   []string
	}{
		{
			name:  "nodejs_compat",
			flags: `"nodejs_compat"`,
			want:  []string{`from "node:fs"`, `from "node:path"`, `from "node:os"`, `from "node:events"`, `from "node:crypto"`, `"default" in`},
			not:   []string{`__require("`, `Dynamic require`, `needs Node.js compatibility`},
		},
		{
			name:  "nodejs_compat_v2",
			flags: `"nodejs_compat_v2"`,
			want:  []string{`from "node:fs"`},
			not:   []string{`__require("`},
		},
		{
			name:  "none",
			flags: ``,
			want: []string{
				`require("fs") in fakedotenv needs Node.js compatibility: add "nodejs_compat" to compatibility_flags`,
				`require("os") in fakedotenv`,
				`require("crypto") in @scope/hasher`,
			},
			not: []string{`__require("`, `from "node:fs"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := writeRequireFixture(t, tc.flags)
			out := filepath.Join(t.TempDir(), "go.js")
			nc := NodeCompat(strings.Split(strings.ReplaceAll(tc.flags, `"`, ""), ","))
			if _, err := Build(Options{Dir: dir, Main: "index.js", Outfile: out, NodeCompat: nc}); err != nil {
				t.Fatal(err)
			}
			gb, _ := os.ReadFile(out)
			for _, w := range tc.want {
				if !bytes.Contains(gb, []byte(w)) {
					t.Errorf("bundle lacks %s:\n%s", w, gb)
				}
			}
			for _, n := range tc.not {
				if bytes.Contains(gb, []byte(n)) {
					t.Errorf("bundle has %s:\n%s", n, gb)
				}
			}
			node, err := exec.LookPath("node")
			if err != nil {
				return
			}
			root, _ := filepath.Abs("../..")
			if _, err := os.Stat(filepath.Join(root, "node_modules", "esbuild")); err != nil {
				return // build.mjs needs esbuild from node_modules (make deps)
			}
			jsOut := filepath.Join(t.TempDir(), "js.js")
			cmd := exec.Command(node, filepath.Join(root, "js", "packages", "create-tailworks", "lib", "build.mjs"), dir, jsOut)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build.mjs: %v %s", err, b)
			}
			jb, _ := os.ReadFile(jsOut)
			if !bytes.Equal(gb, jb) {
				t.Fatalf("Go bundle differs from build.mjs:\n--- go\n%s\n--- build.mjs\n%s", gb, jb)
			}
		})
	}
}

func TestImporterPackage(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/app/node_modules/dotenv/lib/main.js":                        "dotenv",
		"/app/node_modules/@scope/pkg/index.js":                       "@scope/pkg",
		"/app/node_modules/a/node_modules/b/x.js":                     "b",
		"/app/src/legacy.cjs":                                         "src/legacy.cjs",
		"/elsewhere/x.js":                                             "/elsewhere/x.js",
		"/app/node_modules/.pnpm/dotenv@16.4.5/node_modules/dotenv/x": "dotenv",
	} {
		if got := importerPackage("/app", in); got != want {
			t.Errorf("importerPackage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"fs":          "fs",
		"fs/promises": "fs/promises",
		"node:fs":     "node:fs"[5:],
		"node:sqlite": "sqlite",
		"dotenv":      "",
		"./fs":        "",
		"Fs":          "",
	} {
		if got := builtinName(in); got != want {
			t.Errorf("builtinName(%q) = %q, want %q", in, got, want)
		}
	}
}
