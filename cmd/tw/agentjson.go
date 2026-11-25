package main

// Output an agent can parse (issue #25, docs/cli-json.md): the shape of
// --json errors, and the NDJSON events long-running commands write.
//
//   - Every error is one line on stderr: {"error": {code, message, hint,
//     doc_url, detail?, fix?, trace_id?}}, code from docs/errors.md.
//   - A command's result is one JSON document on stdout
//     (schema/cli.schema.json names each command's).
//   - deploy writes progress as NDJSON events on stderr ({"event": …});
//     dev and logs -f, which don't end on their own, write NDJSON on stdout.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/internal/termsafe"
	"github.com/rosskukulinski/tailworks/netd"
)

// wantsJSON reports whether args (before a `--`) ask for --json, so errors
// raised before a command parses its flags (unknown command, bad flag) are
// JSON too.
func wantsJSON(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "--json", "-json", "--json=true", "-json=true":
			return true
		}
	}
	return false
}

// jsonError fills in the fields every --json error carries: hint (the fix
// lines, or where to read about the code) and doc_url.
func jsonError(e *cliErr) *cliErr {
	c := *e
	c.DocURL = netd.DocBase + c.Code
	switch {
	case len(c.Fix) > 0:
		c.Hint = strings.Join(c.Fix, "\n")
	default:
		for _, l := range c.Lines {
			if strings.HasPrefix(l, "usage: ") {
				c.Hint = l
			}
		}
		if c.Hint == "" {
			c.Hint = "see " + c.DocURL
		}
	}
	return &c
}

// events writes NDJSON events: one compact JSON object per line with
// "event" (the kind) and "ts" (RFC 3339, UTC) first. A nil *events
// writes nothing, so callers emit unconditionally.
type events struct {
	mu sync.Mutex
	w  io.Writer
}

// newEvents returns an event writer to w when on (--json), else nil.
func newEvents(on bool, w io.Writer) *events {
	if !on {
		return nil
	}
	return &events{w: w}
}

// emit writes one event; fields are merged after "event" and "ts".
func (ev *events) emit(kind string, fields map[string]any) {
	if ev == nil {
		return
	}
	m := map[string]any{}
	for k, v := range fields {
		m[k] = v
	}
	m["event"] = kind
	m["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"event": "error", "ts": m["ts"], "error": jsonError(fail("internal", err.Error()))})
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	fmt.Fprintln(ev.w, netd.Redact(string(termsafe.JSON(b))))
}

// error writes an "error" event carrying the --json error object.
func (ev *events) error(err error) {
	ev.emit("error", map[string]any{"error": jsonError(asCLIErr(err))})
}

// lineWriter turns written text into one "log" event per line (for
// subprocess output such as docker buildx under deploy --json).
type lineWriter struct {
	ev     *events
	fields map[string]any
	buf    []byte
	// splitHost: a "[<host>] " prefix (remote replica logs) becomes "host".
	splitHost bool
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		l.line(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lineWriter) line(s string) {
	f := map[string]any{}
	for k, v := range l.fields {
		f[k] = v
	}
	s = strings.TrimRight(s, "\r")
	if l.splitHost && strings.HasPrefix(s, "[") {
		if h, rest, ok := strings.Cut(s[1:], "] "); ok && h != "" && !strings.ContainsAny(h, " []") {
			f["host"], s = h, rest
		}
	}
	f["line"] = s
	l.ev.emit("log", f)
}

// Flush writes a trailing partial line.
func (l *lineWriter) Flush() {
	if len(l.buf) > 0 {
		l.line(string(l.buf))
		l.buf = nil
	}
}

// logsOut is where `tw logs` writes: stdout escaped per line, or with --json one
// {"event": "log", "line", "workload" | "component", "host"?} per line. A
// linked machine's replica lines arrive as "[<host>] …"; that prefix
// becomes "host".
func logsOut(on bool, pos []string, comp string, remote bool) (io.Writer, func()) {
	if !on {
		// workerd.log, app.log and the daemons' logs carry tenant and
		// internet-controlled text: escape it per line (internal/termsafe).
		sw := termsafe.NewWriter(os.Stdout)
		return sw, func() { sw.Flush() }
	}
	fields := map[string]any{}
	switch {
	case len(pos) == 1 && comp == "":
		fields["workload"] = pos[0]
	default:
		fields["component"] = comp
	}
	lw := &lineWriter{ev: newEvents(true, os.Stdout), fields: fields, splitHost: remote}
	return lw, lw.Flush
}

// devReadyFields is `tw dev --json`'s "ready" event: the devResult fields
// (url, serves, workload, node, as, timingsMs) plus the app's tags.
func devReadyFields(res devResult, a *app) map[string]any {
	m := map[string]any{}
	b, _ := json.Marshal(res)
	json.Unmarshal(b, &m)
	m["tags"] = a.Cfg.Tailnet.Tags
	return m
}
