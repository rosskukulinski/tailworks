package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Saved `tw up` flags (#456). keepSavedUpFlags (up_saved.go) re-applies
// them from tw.json on every later `tw up` and on `tw upgrade`'s restart, so
// `tw up` and `tw status` say so on every run: the ones that weaken
// isolation or are for development only on a "! unsafe" line, the
// documented modes on a neutral "› saved" line.

// savedFlag is one saved tw up flag worth reporting.
type savedFlag struct {
	name   string
	why    string // what it means, for the human line
	unsafe bool
	set    func(Config) bool
	// clear is how to drop it; "" = tw down, then tw up --<name>=false.
	clear string
}

var savedFlags = []savedFlag{
	{name: "insecure-shared-host", why: "tenants NOT isolated", unsafe: true, set: func(c Config) bool { return c.InsecureSharedHost }},
	{name: "allow-experimental-flags", why: "experimental workerd flags allowed", unsafe: true, set: func(c Config) bool { return c.AllowExperimentalFlags }},
	{name: "local-dev", why: "dev-only transport", unsafe: true, set: func(c Config) bool { return c.LocalDev }, clear: "fixed for this state dir"},
	{name: "ephemeral", set: func(c Config) bool { return c.Ephemeral }},
	{name: "allow-sibling-egress", set: func(c Config) bool { return c.AllowSiblingEgress }},
}

// unsafeFlags is upResult's and statusResult's unsafeFlags: the saved
// unsafe tw up flags, without the dashes; [] when none.
func unsafeFlags(c Config) []string {
	out := []string{}
	for _, f := range savedFlags {
		if f.unsafe && f.set(c) {
			out = append(out, f.name)
		}
	}
	return out
}

// printSavedFlags prints the "! unsafe" and "› saved" lines, the label
// padded to width (9 in tw up, 12 in tw status). hostInsecure is what the
// running host reports for --insecure-shared-host (nil: not known); a
// mismatch with tw.json is marked.
func printSavedFlags(c Config, dir string, width int, hostInsecure *bool) {
	var unsafe, saved, clears []string
	for _, f := range savedFlags {
		if !f.set(c) {
			continue
		}
		if !f.unsafe {
			saved = append(saved, "--"+f.name)
			continue
		}
		u := "--" + f.name + " (" + f.why + ")"
		if f.clear != "" {
			u = "--" + f.name + " (" + f.why + "; " + f.clear + ")"
		} else {
			clears = append(clears, "--"+f.name+"=false")
		}
		unsafe = append(unsafe, u)
	}
	where := filepath.Join(dir, "tw.json")
	if len(unsafe) > 0 {
		line := strings.Join(unsafe, ", ") + " · saved in " + where
		if len(clears) > 0 {
			line += "; clear with tw down, then tw up " + strings.Join(clears, " ")
		}
		fmt.Printf("! %-*s%s\n", width, "unsafe", line)
	}
	if hostInsecure != nil && *hostInsecure != c.InsecureSharedHost {
		fmt.Printf("! %-*sthe running host has --insecure-shared-host=%v but %s says %v: restart tailworks (tw down, then tw up) to apply it\n",
			width, "unsafe", *hostInsecure, where, c.InsecureSharedHost)
	}
	if len(saved) > 0 {
		fmt.Printf("› %-*s%s · kept from %s on every tw up; tw up --<flag>=false drops one\n", width, "saved", strings.Join(saved, ", "), where)
	}
}
