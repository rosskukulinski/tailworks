package main

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/container"
)

// Wrangler "containers" keys tailworks honours or drops (issue #496), and
// the docker buildx check before tw deploy builds Dockerfile images.

// rebaseContainerImage rebases one wrangler containers[].images.<name>
// entry ({dockerfile | image, build_context, build_vars}) for
// rebaseWranglerPaths.
func rebaseContainerImage(im map[string]any, rebase, rebaseSource func(string) string) {
	if s, ok := im["dockerfile"].(string); ok {
		im["dockerfile"] = rebase(s)
	}
	if s, ok := im["image"].(string); ok {
		im["image"] = rebaseSource(s)
	}
	if s, ok := im["build_context"].(string); ok {
		im["build_context"] = rebase(s)
	}
}

// containerKeyWarnings are wranglerWarnings' lines for the "containers"
// entry keys tailworks ignores: one line per key, however many entries set
// it. scheduling_policy "default" or "durable_object" (what Cloudflare's
// examples set) is what tailworks does, so it gets no line.
func containerKeyWarnings(file string, cfg map[string]any) []string {
	xs, _ := cfg["containers"].([]any)
	seen := map[string]bool{}
	for _, x := range xs {
		m, _ := x.(map[string]any)
		for k, v := range m {
			if k == "scheduling_policy" {
				if p, _ := v.(string); p == "default" || p == "durable_object" {
					continue
				}
				seen[k] = true
				continue
			}
			if !slices.Contains(container.HonouredKeys, k) {
				seen[k] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		n := "ignored by tailworks"
		if k == "scheduling_policy" {
			n = `ignored: tailworks runs one container per Durable Object ("durable_object")`
		}
		out = append(out, fmt.Sprintf("%s: containers[].%s: %s", file, k, n))
	}
	return out
}

// containerBuildxCheck fails tw deploy before any build when docker buildx
// is missing (it builds the app's Dockerfile images, ADR-032 D3).
var containerBuildxCheck = func(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "buildx", "version").CombinedOutput()
	if err == nil {
		return nil
	}
	why := strings.TrimSpace(firstLine(string(out)))
	if why == "" {
		why = err.Error()
	}
	return fail("image_build_failed", "tw deploy builds this app's Dockerfile images with docker buildx, which is not usable here: "+why).
		withFix("install Docker with the buildx plugin (Docker Desktop, or docker-buildx-plugin on Linux), then: docker buildx version")
}
