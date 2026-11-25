package main

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

// devDefaultPort is wrangler dev's default port, and tw dev's.
const devDefaultPort = 8787

// resolveDevPort picks tw dev's local port (#473). A port someone chose
// (--port, else the wrangler config's dev.port) is used as given and fails
// when busy, as wrangler dev does. Only when nothing chose one and 8787 is
// busy does tw dev take a free port, with one stderr line naming both.
func resolveDevPort(flagPort int, flagSet bool, a *app, warn io.Writer) (int, error) {
	if flagSet {
		return flagPort, checkDevPort(flagPort, warn)
	}
	if p := devWranglerPort(a); p > 0 {
		if err := checkDevPort(p, warn); err != nil {
			if ce, ok := err.(*cliErr); ok {
				ce.Message += " (dev.port in " + a.WranglerFile + ")"
			}
			return 0, err
		}
		return p, nil
	}
	if err := checkDevPort(flagPort, warn); err == nil {
		return flagPort, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, checkDevPort(flagPort, warn)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	fmt.Fprintf(warn, "! port %d is in use on 127.0.0.1; tw dev uses %d instead (--port or wrangler dev.port picks one)\n", flagPort, p)
	return p, checkDevPort(p, warn)
}

// devWranglerPort is the app's wrangler dev.port, 0 for none. dev is a
// top-level-only key (an environment can't set it), so it is read from the
// config file as written, whatever --env is.
func devWranglerPort(a *app) int {
	if a.WranglerFile == "" {
		return 0
	}
	cfg, err := readWranglerConfig(filepath.Join(a.Dir, a.WranglerFile))
	if err != nil {
		return 0
	}
	p, _ := devPortValue(cfg["dev"])
	return p
}

// devPortValue reads dev.port from a wrangler dev section: the port, and
// whether dev.port is set at all (a set but invalid port is 0, true).
func devPortValue(dev any) (int, bool) {
	m, _ := dev.(map[string]any)
	v, ok := m["port"]
	if !ok {
		return 0, false
	}
	f, isNum := v.(float64) // JSON numbers, and TOML's via jsonShaped
	if !isNum || f != float64(int(f)) || f < 1 || f > 65535 {
		return 0, true
	}
	return int(f), true
}

// devWranglerNote is wranglerKeyNote's line for the dev section: dev.port
// is tw dev's port; the rest is ignored. dev.ip, local_protocol and host
// stay ignored on purpose: tw dev serves plain HTTP on 127.0.0.1 only.
func devWranglerNote(v any) string {
	m, _ := v.(map[string]any)
	var notes []string
	if p, set := devPortValue(v); set && p == 0 {
		notes = append(notes, fmt.Sprintf("dev.port %v is not a port number; tw dev uses %d", m["port"], devDefaultPort))
	}
	var other []string
	for k := range m {
		if k != "port" {
			other = append(other, "dev."+k)
		}
	}
	sort.Strings(other)
	if len(other) > 0 {
		notes = append(notes, strings.Join(other, ", ")+" ignored by tailworks (tw dev serves HTTP on 127.0.0.1 only; it honours dev.port)")
	}
	if m == nil {
		notes = append(notes, "ignored by tailworks")
	}
	return strings.Join(notes, "; ")
}
