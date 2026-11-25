package gen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

func secretsApp() *bundle.App {
	return &bundle.App{
		Name: "w", Main: "index.js", CompatDate: "2026-04-01",
		Vars:    map[string]json.RawMessage{"GREETING": json.RawMessage(`"hi"`), "API_KEY": json.RawMessage(`"from-vars"`)},
		Modules: map[string][]byte{"index.js": []byte("export default { fetch() { return new Response('ok'); } };")},
		Classes: map[string]*bundle.Class{},
		Secrets: map[string]string{"API_KEY": "s\"e\\c\nret", "OTHER": "o"},
	}
}

// TestGenerateSecrets: secrets become text bindings of the tenant worker
// (overriding a var of the same name) and enter the facets' vars as JSON
// strings, only in the instance directory. app is not modified.
func TestGenerateSecrets(t *testing.T) {
	app := secretsApp()
	dir := filepath.Join(t.TempDir(), "i")
	if _, err := gen.Generate(app, gen.Options{Dir: dir, DOStorage: filepath.Join(dir, "do"), DeploymentID: "dp_1", Workload: "w"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.capnp"))
	c := string(cfg)
	for _, want := range []string{`(name = "API_KEY", text = "s\"e\\c\nret")`, `(name = "OTHER", text = "o")`, `(name = "GREETING", json = "\"hi\"")`} {
		if !strings.Contains(c, want) {
			t.Errorf("config.capnp lacks %s:\n%s", want, c)
		}
	}
	if strings.Contains(c, "from-vars") {
		t.Error("the var a secret overrides is still bound")
	}
	var tc struct {
		Vars map[string]any `json:"vars"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "tw-config.json"))
	if err := json.Unmarshal(b, &tc); err != nil || tc.Vars["API_KEY"] != "s\"e\\c\nret" || tc.Vars["GREETING"] != "hi" || tc.Vars["OTHER"] != "o" {
		t.Fatalf("tw-config.json vars: %v %v", tc.Vars, err)
	}
	if string(app.Vars["API_KEY"]) != `"from-vars"` || len(app.Vars) != 2 {
		t.Fatalf("Generate modified app.Vars: %v", app.Vars)
	}
	for _, f := range []string{"config.capnp", "tw-config.json"} {
		if st, _ := os.Stat(filepath.Join(dir, f)); st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, st.Mode().Perm())
		}
	}
}

func TestGenerateSecretNames(t *testing.T) {
	for name, mod := range map[string]func(*bundle.App){
		"reserved":        func(a *bundle.App) { a.Secrets = map[string]string{"__TW": "x"} },
		"not identifier":  func(a *bundle.App) { a.Secrets = map[string]string{"A-B": "x"} },
		"do binding name": func(a *bundle.App) { a.Bindings = []bundle.DOBinding{{Name: "OTHER", Class: "C"}} },
	} {
		app := secretsApp()
		mod(app)
		_, err := gen.Generate(app, gen.Options{Dir: filepath.Join(t.TempDir(), "i"), DOStorage: filepath.Join(t.TempDir(), "do"), DeploymentID: "dp_1", Workload: "w"})
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "s\"e") {
			t.Errorf("%s: error carries a value: %v", name, err)
		}
	}
}
