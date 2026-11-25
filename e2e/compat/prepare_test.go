//go:build e2ecompat

package compat

import (
	"os"
	"regexp"
	"sync"
	"testing"
)

// selected is Projects() filtered by TW_COMPAT_ONLY (a regexp on the ID).
func selected(t *testing.T) []*Project {
	ps := Projects()
	re := os.Getenv("TW_COMPAT_ONLY")
	if re == "" {
		return ps
	}
	rx := regexp.MustCompile(re)
	var out []*Project
	for _, p := range ps {
		if rx.MatchString(p.ID) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("TW_COMPAT_ONLY=%q matches no project", re)
	}
	return out
}

// prepareAll prepares ps, par at a time (npm and the builds dominate).
func prepareAll(ps []*Project, tag string, par int) map[string]*Prepared {
	out := map[string]*Prepared{}
	var mu sync.Mutex
	errs := map[string]error{}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	// Projects from one repo share a checkout: fetch each repo first.
	repos := map[string]string{}
	for _, p := range ps {
		repos[p.Repo] = p.Commit
	}
	for repo, commit := range repos {
		checkout(repo, commit)
	}
	for _, p := range ps {
		wg.Add(1)
		go func(p *Project) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pr, err := Prepare(p, tag)
			mu.Lock()
			out[p.ID] = pr
			if err != nil {
				errs[p.ID] = err
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	for id, err := range errs {
		out[id].Err = err
	}
	return out
}

// TestCompatPrepare only fetches, installs and builds (no tailnet):
// `make e2e-compat-prepare` warms the cache and shows build breakage.
func TestCompatPrepare(t *testing.T) {
	ps := selected(t)
	prs := prepareAll(ps, "tag:tw-wl-compat", 4)
	for _, p := range ps {
		pr := prs[p.ID]
		if pr.Err != nil {
			t.Errorf("%s: %v", p.ID, pr.Err)
			continue
		}
		t.Logf("%s: ready in %v → %s", p.ID, pr.Dur.Round(1e6), pr.DeployDir)
	}
}
