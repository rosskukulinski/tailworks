package main

import (
	"context"
	"io"
	"time"

	"github.com/rosskukulinski/twd/internal/obs"
)

// devRecordsEvery is how often twd dev checks the request log.
const devRecordsEvery = 100 * time.Millisecond

// followDevRecords prints the workload's request records (one line per
// request, its console.* lines and uncaught exceptions with their stacks)
// as hostd writes them, like `wrangler dev` prints each request and its
// console output. Without it, console.log and a 500's stack were only in
// a temp dir that twd dev deletes on exit. With --json (ev != nil) each
// record is a "request" event instead. It returns when ctx ends.
func followDevRecords(ctx context.Context, path string, w io.Writer, ev *events) {
	fl := obs.NewRecordFollower(path, 0)
	defer fl.Close()
	tick := time.NewTicker(devRecordsEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		recs, err := fl.Next()
		if err != nil {
			continue // a rotated or not-yet-created file: try again
		}
		for _, r := range recs {
			if ev != nil {
				ev.emit("request", map[string]any{"record": r})
				continue
			}
			io.WriteString(w, r.Text())
		}
	}
}
