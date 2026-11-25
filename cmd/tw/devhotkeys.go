package main

// tw dev's hotkeys (issue #42), like `wrangler dev`'s: b opens the app in
// a browser, e opens the dev console (devconsole.go), x or q stops. Only when stdin is the terminal's foreground
// and --json is off; the terminal goes to non-canonical, no-echo mode (one
// key, no Enter) and is restored on exit. Ctrl-C still works (ISIG stays).

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// devHotkeysHint is printed under the ready line when the hotkeys are on.
const devHotkeysHint = "  [b] open in a browser · [x] or [q] stop"

// devHotkeysConsoleHint is the hint when the dev console is on.
const devHotkeysConsoleHint = "  [b] open in a browser · [e] open the data console · [x] or [q] stop"

// devHotkeyAction is what a key does.
type devHotkeyAction int

const (
	hotkeyNone devHotkeyAction = iota
	hotkeyBrowser
	hotkeyQuit
	// e: the dev console (devconsole.go).
	hotkeyConsole
)

// devHotkey maps a key to its action.
func devHotkey(c byte) devHotkeyAction {
	switch c {
	case 'b', 'B':
		return hotkeyBrowser
	case 'x', 'X', 'q', 'Q':
		return hotkeyQuit
	case 'e', 'E':
		return hotkeyConsole
	}
	return hotkeyNone
}

// devHotkeysOn reports whether tw dev should read hotkeys: stdin is a
// terminal, this process is its foreground, and output isn't --json.
func devHotkeysOn(json bool) bool {
	if json || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	pg, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	return err == nil && pg == unix.Getpgrp()
}

// startDevHotkeys puts the terminal in one-key mode and reads keys: b opens
// url, e opens consoleURL (when not ""), x/q closes quit. restore puts the terminal back (call it on exit).
func startDevHotkeys(url, consoleURL string, out io.Writer) (quit <-chan struct{}, restore func()) {
	saved, err := stty("-g")
	if err != nil {
		return nil, func() {}
	}
	if _, err := stty("-icanon", "-echo", "min", "1"); err != nil {
		return nil, func() {}
	}
	restore = func() { stty(strings.TrimSpace(saved)) }
	q := make(chan struct{})
	go func() {
		b := make([]byte, 1)
		for {
			if n, err := os.Stdin.Read(b); err != nil || n == 0 {
				return
			}
			switch devHotkey(b[0]) {
			case hotkeyBrowser:
				if err := openBrowser(url); err != nil {
					fmt.Fprintf(out, "! can't open a browser (%v): %s\n", err, url)
				}
			case hotkeyConsole:
				if consoleURL == "" {
					continue
				}
				if err := openBrowser(consoleURL); err != nil {
					fmt.Fprintf(out, "! can't open a browser (%v): %s\n", err, consoleURL)
				}
			case hotkeyQuit:
				close(q)
				return
			}
		}
	}()
	return q, restore
}

// stty runs stty on the terminal (stdin) and returns its output.
func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	b, err := cmd.Output()
	return string(b), err
}

// openBrowser opens url in the default browser.
func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// devInteractive starts what `tw dev` adds after its ready line: the
// inspector proxy (--inspector-port, devinspector.go), the dev console's
// line (consoleURL, "" when it is off; devconsole.go) and the hotkeys.
// quit closes on x/q (nil: no hotkeys); stop undoes both.
func devInteractive(port int, target func() string, url, consoleURL string, json bool, out io.Writer, ev *events) (quit <-chan struct{}, stop func(), err error) {
	var stops []func()
	stop = func() {
		for _, s := range stops {
			s()
		}
	}
	if port > 0 {
		srv, addr, err := devInspector(port, target)
		if err != nil {
			return nil, stop, err
		}
		stops = append(stops, func() { srv.Close() })
		ev.emit("inspector", map[string]any{"addr": addr, "ws": "ws://" + addr})
		fmt.Fprintf(out, "  inspector on %s · Chrome DevTools: chrome://inspect, Configure, add %s · VS Code: attach to port %d (docs/dev-debugging.md)\n", addr, addr, port)
	}
	if consoleURL != "" {
		ev.emit("console", map[string]any{"url": consoleURL})
		fmt.Fprintf(out, "  console on %s · KV keys and D1 rows of this run, read-only\n", consoleURL)
	}
	if devHotkeysOn(json) {
		var restore func()
		quit, restore = startDevHotkeys(url, consoleURL, out)
		if quit != nil {
			stops = append(stops, restore)
			hint := devHotkeysHint
			if consoleURL != "" {
				hint = devHotkeysConsoleHint
			}
			fmt.Fprintln(out, hint)
		}
	}
	return quit, stop, nil
}
