package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestDevRequireBuiltins (issue #356): a CommonJS dependency that
// require()s Node builtins, the way dotenv does, bundles with the built-in
// bundler and starts in workerd with nodejs_compat. Without the flag the
// Worker fails to start with an error naming the package and the flag, not
// esbuild's `Dynamic require of "node:fs" is not supported`.
func TestDevRequireBuiltins(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	write := func(app, flags string) string {
		dir := filepath.Join(work, app)
		for name, s := range map[string]string{
			"tailworks.jsonc": `{ "name": "` + app + `", "tailnet": { "tags": ["tag:tw-wl-` + app + `"] } }`,
			"wrangler.jsonc":  `{"name": "` + app + `", "main": "index.js", "compatibility_date": "2026-06-11", "compatibility_flags": [` + flags + `]}`,
			"index.js": `import "fakedotenv/config";
export default { fetch() { return Response.json({ fake: process.env.FAKE }); } };
`,
			"node_modules/fakedotenv/package.json": `{ "name": "fakedotenv", "main": "lib/main.js" }`,
			"node_modules/fakedotenv/config.js":    `(function () { require("./lib/main").config(); })();`,
			"node_modules/fakedotenv/lib/main.js": `const fs = require('fs')
const path = require('path')
const { EventEmitter } = require('events')
module.exports.config = function () {
  process.env.FAKE = [typeof fs.readFileSync, path.join('a', 'b'), typeof EventEmitter].join(',')
}
`,
		} {
			p := filepath.Join(dir, name)
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r struct {
		Smoke map[string]any `json:"smoke"`
	}
	e.MustJSON(&r, "dev", write("req", `"nodejs_compat"`), "--once", "--port", "0")
	if r.Smoke["fake"] != "function,a/b,function" {
		t.Fatalf("tw dev --once, require() of builtins with nodejs_compat: %+v", r.Smoke)
	}

	e2 := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state2"), WorkDir: work}
	stdout, stderr, _, err := e2.Tw("dev", write("noreq", ""), "--once", "--port", "0")
	if err == nil {
		t.Fatalf("tw dev --once without nodejs_compat started:\n%s", stdout)
	}
	if out := stdout + stderr; !strings.Contains(out, `require(\"fs\") in fakedotenv needs Node.js compatibility`) && !strings.Contains(out, `require("fs") in fakedotenv needs Node.js compatibility`) {
		t.Fatalf("tw dev --once without nodejs_compat: want the package and flag named, got:\n%s\n%s", stdout, stderr)
	}
}
