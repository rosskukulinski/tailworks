package compat

import (
	"strings"
	"testing"
)

// recordAll is a Published in which every published project has the
// outcome its Expect says, in both modes.
func recordAll() *Published {
	pub := &Published{}
	for _, mode := range []string{"dev", "deployed"} {
		run := &RunRecord{Date: "2026-01-02", Commit: "0123456789abcdef", Platform: "test/arch", Results: map[string]Recorded{}}
		for _, p := range Projects() {
			if p.Section == "" {
				continue
			}
			out := "PASS"
			if p.ExpectIn(mode).Kind != ExpectPass {
				out = "XFAIL"
			}
			run.Results[p.ID] = Recorded{Outcome: out, Source: p.source()}
		}
		if mode == "dev" {
			pub.Dev = run
		} else {
			pub.Deployed = run
		}
	}
	return pub
}

func TestUpstreamListIsValid(t *testing.T) {
	if err := Validate(Projects(), nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range Projects() {
		if p.Section != "" {
			seen[p.Repo+"/"+p.Dir] = true
		}
	}
	for _, n := range UpstreamNotRun() {
		if n.Why == "" || len(n.Dirs) == 0 || !commitRe.MatchString(n.Commit) {
			t.Errorf("not-run entry %+v needs a reason, directories and a full commit", n)
		}
		for _, d := range n.Dirs {
			if key := n.Repo + "/" + n.Base + d; seen[key] {
				t.Errorf("%s is both a project and not run", key)
			} else {
				seen[key] = true
			}
		}
	}
}

func TestVerify(t *testing.T) {
	pub := recordAll()
	if err := pub.Verify(); err != nil {
		t.Fatal(err)
	}
	// The deployed run is optional; the dev run is not.
	if err := (&Published{Dev: pub.Dev}).Verify(); err != nil {
		t.Fatal(err)
	}
	if err := (&Published{Deployed: pub.Deployed}).Verify(); err == nil {
		t.Error("no dev run: want an error")
	}
	for name, breakIt := range map[string]func(*Published){
		"a project without a dev result": func(p *Published) { delete(p.Dev.Results, "mcph-route") },
		"a result for another commit": func(p *Published) {
			r := p.Dev.Results["mcph-route"]
			r.Source = "vercel/mcp-handler/examples@000000000000"
			p.Dev.Results["mcph-route"] = r
		},
		"an outcome the project doesn't expect": func(p *Published) {
			r := p.Deployed.Results["py-workflows"]
			r.Outcome = "PASS"
			p.Deployed.Results["py-workflows"] = r
		},
		"a result for an unpublished project": func(p *Published) {
			p.Dev.Results["c3-hello-world"] = Recorded{Outcome: "PASS", Source: "x"}
		},
	} {
		p := recordAll()
		breakIt(p)
		if err := p.Verify(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestUpdateDoc(t *testing.T) {
	pub := recordAll()
	var md strings.Builder
	md.WriteString("# Doc\n\nbefore\n\n")
	for _, s := range Sections {
		md.WriteString(marker(s.ID, "begin") + "\nstale, edited by hand\n" + marker(s.ID, "end") + "\n\nbetween\n\n")
	}
	out, err := UpdateDoc([]byte(md.String()), pub)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"before", "between", "| [`route.ts`](https://github.com/vercel/mcp-handler/blob/" + mcpHandlerCommit + "/examples/route.ts)",
		"**pass with change**", "run 2026-01-02 on test/arch, tailworks `0123456789ab`", "Not run:"} {
		if !strings.Contains(got, want) {
			t.Errorf("the updated doc lacks %q", want)
		}
	}
	if strings.Contains(got, "edited by hand") {
		t.Error("the stale block survived")
	}
	// Rendering is stable: a second update changes nothing.
	again, err := UpdateDoc(out, pub)
	if err != nil || string(again) != got {
		t.Errorf("a second update changed the doc (%v)", err)
	}
	if _, err := UpdateDoc([]byte("# no markers\n"), pub); err == nil {
		t.Error("a doc without the blocks: want an error")
	}
}
