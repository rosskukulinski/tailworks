package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestCapnpConfigNotInjectable pins the capnp half of ledger claim C20:
// tenant-chosen strings (var values) are quoted into workerd's config and
// cannot add a binding or service, and a var name that isn't an identifier
// is refused.
func TestCapnpConfigNotInjectable(t *testing.T) {
	const evil = "x\"),\n    (name = \"EVIL\", service = \"hostd\"),\n    (name = \"Y\", text = \"\\\x00\x7f"
	gen1 := func(name string) (string, error) {
		t.Helper()
		wr := `{"name": "w", "main": "index.js", "compatibility_date": "2026-04-01", "vars": {` + jsonStr(name) + `: ` + jsonStr(evil) + `}}`
		app, err := bundle.Parse(map[string][]byte{"index.js": []byte("export default {}"), "wrangler.jsonc": []byte(wr)})
		if err != nil {
			return "", err
		}
		dir := t.TempDir()
		res, err := gen.Generate(app, gen.Options{Dir: filepath.Join(dir, "i1"), DOStorage: filepath.Join(dir, "do"), DeploymentID: "d1", Workload: "w"})
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(res.Config)
		return string(b), err
	}
	cfg, err := gen1("GOOD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, `(name = "EVIL"`) || strings.Contains(cfg, "\x00") || strings.Contains(cfg, "\x7f") {
		t.Fatalf("a var value broke out of its capnp string:\n%s", cfg)
	}
	if !strings.Contains(cfg, `(name = "GOOD", json = `) {
		t.Fatalf("the var binding is missing:\n%s", cfg)
	}
	for _, bad := range []string{`A"B`, "A B", "__TW_X", "1A", "A\nB"} {
		if _, err := gen1(bad); err == nil {
			t.Errorf("var name %q accepted", bad)
		}
	}
}

func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u00`)
			b.WriteByte("0123456789abcdef"[r>>4])
			b.WriteByte("0123456789abcdef"[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
