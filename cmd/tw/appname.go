package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// nameNotes are the `›` notes about where the workload name comes from
// (issue #447). The precedence is unchanged (--name, then tailworks.jsonc
// "name", then the wrangler config's "name", then the directory); these
// only say so when it can surprise:
//
//   - both files set a name and they differ, so a wrangler-style rename
//     doesn't take effect;
//   - tailnet.tags is exactly tag:tw-wl-<other>, named after none of this
//     app's names, as when an app is copied and renamed: it would share
//     every grant the other app's tag has.
//
// flagName is --name; twName is tailworks.jsonc's "name".
func (a *app) nameNotes(flagName, twName string) []string {
	var out []string
	if flagName == "" && twName != "" && a.WranglerName != "" && twName != a.WranglerName {
		out = append(out, fmt.Sprintf("tailworks.jsonc name %q wins over %s name %q, so the workload is %q; drop \"name\" from tailworks.jsonc to use %s's",
			twName, a.WranglerFile, a.WranglerName, a.Name, a.WranglerFile))
	}
	if a.Cfg == nil || len(a.Cfg.Tailnet.Tags) != 1 {
		return out
	}
	tag := a.Cfg.Tailnet.Tags[0]
	other, ok := strings.CutPrefix(tag, "tag:tw-wl-")
	if !ok || other == "" {
		return out
	}
	for _, n := range []string{a.Name, twName, a.WranglerName, filepath.Base(a.Dir)} {
		if n != "" && (other == n || other == workloadLabel(n)) {
			return out
		}
	}
	out = append(out, fmt.Sprintf("tailnet.tags is %s, named after another app (%q): this app (%q) shares every grant to that tag; for tags of its own, set \"tags\": [\"tag:tw-wl-%s\"] (tw policy prints what to add)",
		tag, other, a.Name, a.Name))
	return out
}
