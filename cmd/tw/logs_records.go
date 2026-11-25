package main

// `tw logs <workload>` shows the workload's structured request records
// (ADR-045): one line per request (time, method, path, status,
// duration, object, caller, trace) with its console.* lines and exceptions
// indented under it; --json prints the records as JSON Lines; --object,
// --status, --outcome, --errors, --caller and --trace filter them (on the
// host, so -n counts matching records). --console shows the app's raw
// output as before.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/termsafe"
)

// logsOpts are `tw logs`' record flags.
type logsOpts struct {
	console bool
	filter  obs.Filter
}

func logsFlags(f *flags) func() (logsOpts, error) {
	console := f.Bool("console", false, "the app's raw output (workerd.log, the Node runtime's app.log, the microVM console) instead of request records")
	object := f.String("object", "", "only this object (object ID, Class/name, Class or name)")
	status := f.String("status", "", "only this status (404) or class (5xx)")
	outcome := f.String("outcome", "", "only this outcome: ok, exception, canceled, … (wrangler tail's --status)")
	caller := f.String("caller", "", "only callers whose login or node name contains this (public: Funnel requests)")
	trace := f.String("trace", "", "only this trace ID (or a prefix), or audit trace ID")
	errs := f.Bool("errors", false, "only errors (status ≥ 400, an outcome other than ok, or an exception)")
	return func() (logsOpts, error) {
		o := logsOpts{console: *console, filter: obs.Filter{Object: *object, Status: *status, Outcome: *outcome, Caller: *caller, Trace: *trace, Errors: *errs}}
		if o.console && !o.filter.Empty() {
			return o, fail("bad_request", "the record filters (--object, --status, --outcome, --caller, --trace, --errors) don't apply to --console")
		}
		g, err := obs.ParseFilter(o.filter.Values())
		if err != nil {
			return o, fail("bad_request", err.Error())
		}
		o.filter = g
		return o, nil
	}
}

// printRecord writes one record: JSON with --json, else text.
func printRecord(w io.Writer, r *obs.Record) {
	if jsonOut {
		b, _ := json.Marshal(r)
		w.Write(append(termsafe.JSON(b), '\n'))
		return
	}
	io.WriteString(w, r.Text())
}

// localRecords prints a workload's request records from the file on this
// machine (and follows it).
func localRecords(path, workload string, n int, follow bool, f obs.Filter) error {
	recs, pos, err := obs.TailRecords(path, n, f)
	if err != nil && !os.IsNotExist(err) {
		return fail("no_logs", "can't read "+path+": "+err.Error())
	}
	for _, r := range recs {
		printRecord(os.Stdout, r)
	}
	if len(recs) == 0 && !follow && !jsonOut {
		fmt.Fprintf(os.Stderr, "no request records for %s yet%s · the app's raw output: tw logs %s --console\n", workload, filterNote(f), workload)
	}
	if !follow {
		return nil
	}
	fl := obs.NewRecordFollower(path, pos)
	defer fl.Close()
	for {
		recs, err := fl.Next()
		if err != nil {
			return err
		}
		for _, r := range recs {
			if f.Match(r) {
				printRecord(os.Stdout, r)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func filterNote(f obs.Filter) string {
	if f.Empty() {
		return ""
	}
	return " matching the filters"
}

// copyRecords renders a JSON Lines stream of records (mgmt prefixes each
// line with "[<host>] " when a workload has replicas; records carry their
// host anyway). Lines that are not records (a host's "tw: …" note) pass
// through.
func copyRecords(w io.Writer, body io.Reader) error {
	rd := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			l := line
			prefix := []byte(nil)
			if l[0] == '[' {
				if i := bytes.Index(l, []byte("] ")); i > 0 {
					prefix, l = l[:i+2], l[i+2:]
				}
			}
			var r obs.Record
			if json.Unmarshal(l, &r) == nil && r.Kind != "" {
				if jsonOut {
					w.Write(termsafe.JSON(l))
				} else {
					if len(prefix) > 0 {
						io.WriteString(w, termsafe.String(string(prefix)))
					}
					io.WriteString(w, r.Text())
				}
			} else if jsonOut {
				// A host's note ("tw: …"): still one JSON object per line.
				ev := newEvents(true, w)
				ev.emit("log", map[string]any{"line": string(bytes.TrimRight(line, "\r\n"))})
			} else {
				io.WriteString(w, termsafe.Line(string(bytes.TrimRight(line, "\r\n")))+"\n")
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
