package compatdoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNightlyOnlyRowMustSaySo: a ✅/⚠️ row whose tests all run only in a
// nightly or by-hand suite fails unless its Tests cell says "nightly"
// (issue #163).
func TestNightlyOnlyRowMustSaySo(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hostd/a_test.go", "package hostd\n\nfunc TestUnit(t *testing.T) {}\n")
	write("hostd/linux_test.go", "//go:build linux\n\npackage hostd\n\nfunc TestLinux(t *testing.T) {}\n")
	write("internal/tssvc/real_test.go", "//go:build e2ereal\n\npackage tssvc\n\nfunc TestReal(t *testing.T) {}\n")
	write("e2e/sdk/sdk_test.go", "//go:build sdk\n\npackage sdk\n\nfunc TestSDK(t *testing.T) {}\n")
	write("e2e/local/local_test.go", "//go:build e2e\n\npackage local\n\nfunc TestLocal(t *testing.T) {}\n")
	write("e2e/vitest/test/a.test.ts", "it(\"works\", () => {});\n")
	write("js/x.test.js", "test(\"js\", () => {});\n")

	for _, c := range []struct {
		name, status, tests string
		fail                bool
	}{
		{"SDKOnly", "✅", "`e2e/sdk:TestSDK`", true},
		{"PartialSDKOnly", "⚠️", "`e2e/sdk:TestSDK`", true},
		{"TaggedOutsideE2E", "✅", "`internal/tssvc:TestReal`", true},
		{"SaysNightly", "✅", "`e2e/sdk:TestSDK` (nightly: `make test-sdk`)", false},
		{"SaysNightlyCase", "✅", "`e2e/sdk:TestSDK` (Nightly)", false},
		{"OneUnitTest", "✅", "`e2e/sdk:TestSDK`, `hostd:TestUnit`", false},
		{"OSTagIsMakeTest", "✅", "`hostd:TestLinux`", false},
		{"E2ELocalPerPR", "✅", "`e2e/local:TestLocal`", false},
		{"VitestPerPR", "✅", "`e2e/vitest/test/a.test.ts:works`", false},
		{"JSFile", "✅", "`js/x.test.js:js`", false},
		{"MissingIsNotChecked", "❌", "`e2e/sdk:TestSDK`", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			readme := "## Compatibility at a glance\n\n| S | Status |\n|---|---|\n| [x](docs/COMPAT.md#x) | " + c.status + " |\n"
			compat := "| S | Status | Tests |\n|---|---|---|\n| <a id=\"x\"></a>x | " + c.status + " | " + c.tests + " |\n"
			var got []string
			for _, e := range CheckDocs(root, []byte(readme), []byte(compat)) {
				got = append(got, e.Error())
			}
			failed := strings.Contains(strings.Join(got, "\n"), `say "nightly"`)
			if failed != c.fail {
				t.Fatalf("nightly error = %v, want %v; errors:\n%s", failed, c.fail, strings.Join(got, "\n"))
			}
		})
	}
}
