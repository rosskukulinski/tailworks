//go:build e2edataplane

package dataplane

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The R2 checkers (ledger/r2.md, issue #420). R1 models each key as a
// register whose values are unique (every write's bytes name it), with
// three sound checks instead of a full linearizability search: no read of
// a value never written, or written after the read ended; no read of a
// value that a later acknowledged write had replaced before the read
// started; and no read older than what an earlier read already saw. A
// write whose answer was lost (an error, a crash) may take effect any
// time after it was sent, so it can never be "replaced before" anything.

type r2Input struct {
	ops      []r2Op
	faults   []faultEv
	deps     []depEv
	backend  string // fs | s3
	replicas bool
	blobs    int // blobs in the backend at the end
	blobErr  error
}

// never is the effective end of a write whose outcome is unknown.
var never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// outage is a declared mgmt outage: a host cut off from mgmt ("" = every
// host: mgmt itself was down).
type outage struct {
	kind, host string
	start, end time.Time
	crash      bool          // restart-mgmt by SIGKILL (down from start at once)
	down       time.Duration // restart-mgmt: how long mgmt was down
}

var downRE = regexp.MustCompile(`down ([0-9.]+[a-zµ]+)`)

func outagesOf(faults []faultEv) []outage {
	var out []outage
	for _, f := range faults {
		switch f.Kind {
		case "cut-short", "cut-takeover":
			out = append(out, outage{kind: f.Kind, host: f.Host, start: f.Start, end: f.End})
		case "restart-mgmt":
			o := outage{kind: f.Kind, start: f.Start, end: f.End, crash: strings.Contains(f.Detail, "crash=true")}
			if m := downRE.FindStringSubmatch(f.Detail); m != nil {
				o.down, _ = time.ParseDuration(m[1])
			}
			out = append(out, o)
		}
	}
	return out
}

const (
	// staleAfter: after an outage ends, the store's read breaker closes on
	// its next background catch-up (STALE_RETRY_MS 5 s, hostd/gen/js/store.js).
	staleAfter = 8 * time.Second
	// fenceBound: a fenced workload stops 30 s (FenceAfter) after its last
	// heartbeat (every 3 s); 7 s more for the stop itself and load.
	fenceBound = 40 * time.Second
)

// stalenessExcuse says whether a stale answer from host at t is allowed:
// inside a declared mgmt outage of that host (or of mgmt), and for a
// fenced workload no later than fenceBound into a host's cut-off. ok
// false with in true: inside an outage but past the bound.
func stalenessExcuse(outs []outage, host string, t time.Time, fenced bool) (ok, in bool, why string) {
	for _, o := range outs {
		if o.host != "" && o.host != host {
			continue
		}
		if t.Before(o.start) || t.After(o.end.Add(staleAfter)) {
			continue
		}
		in = true
		if fenced && o.host != "" && t.Sub(o.start) > fenceBound {
			why = fmt.Sprintf("%s of %s since %s, %.0fs in", o.kind, o.host, o.start.Format("15:04:05.000"), t.Sub(o.start).Seconds())
			continue
		}
		return true, true, o.kind
	}
	return false, in, why
}

func isWrite(op string) bool {
	switch op {
	case "put", "del", "mpu-create", "mpu-part", "mpu-complete", "mpu-abort":
		return true
	}
	return false
}

type r2w struct {
	o          r2Op
	del        bool
	sent, recv time.Time // recv: never if the outcome is unknown
}

func checkR2All(in r2Input) []result {
	outs := outagesOf(in.faults)
	fenced := !in.replicas
	r1 := result{ID: "R1", Pass: true}
	r2 := result{ID: "R2", Pass: true}
	r3 := result{ID: "R3", Pass: true}
	r4 := result{ID: "R4", Pass: true}
	r5 := result{ID: "R5", Pass: true}
	r6 := result{ID: "R6", Pass: true}
	desc := func(o r2Op) string {
		s := fmt.Sprintf("%s %s %s on %s [%s → %s]", o.ID, o.Op, o.Key, o.Host, o.Sent.Format("15:04:05.000"), o.Recv.Format("15:04:05.000"))
		if o.Val != "" {
			s += " val " + o.Val
		}
		if o.Absent {
			s += " (absent)"
		}
		if o.Err != "" {
			s += " err " + trunc(o.Err, 160)
		}
		if o.Probe {
			s += " (probe)"
		}
		if o.Final {
			s += " (final)"
		}
		return s
	}

	// Values: every write's expected etag names it (heads answer etags).
	etagVal := map[string]string{}
	valKey := map[string]string{}
	partHosts := map[string]map[string]bool{} // multipart value → hosts its parts were written on
	for _, o := range in.ops {
		switch o.Op {
		case "put", "mpu-complete":
			etagVal[o.Etag] = o.Val
			valKey[o.Val] = o.Key
		case "mpu-part":
			if o.OK {
				if partHosts[o.Val] == nil {
					partHosts[o.Val] = map[string]bool{}
				}
				partHosts[o.Val][o.Host] = true
			}
		}
	}
	for i := range in.ops {
		o := &in.ops[i]
		if o.Op == "head" && o.OK && !o.Absent {
			if v, ok := etagVal[o.Etag]; ok {
				o.Val = v
			} else {
				o.Bad = "head answered etag " + o.Etag + ", which no write has"
			}
		}
	}

	// ---- R2/R6: bytes, etags, MD5 checks ----
	nGets, nBytes, staleMissing := 0, 0, 0
	for _, o := range in.ops {
		mp := strings.HasPrefix(o.Key, "m")
		switch {
		case o.Op == "put" && o.BadMD5 && o.OK:
			r2.fail("a put with a wrong MD5 was acknowledged: %s", desc(o))
		case (o.Op == "get" || o.Op == "head") && o.OK && o.Bad != "":
			if mp {
				r6.fail("%s: %s", o.Bad, desc(o))
			} else {
				r2.fail("%s: %s", o.Bad, desc(o))
			}
		case o.OK && o.Bad != "":
			r2.fail("%s: %s", o.Bad, desc(o))
		case o.Op == "get" && !o.OK && in.backend == "s3" && strings.Contains(o.Err, "r2_blob_missing"):
			// A copy of the index that is stale during an outage can
			// name a blob another replica has since replaced and deleted.
			if ok, _, _ := stalenessExcuse(outs, o.Host, o.Sent, !in.replicas); ok {
				staleMissing++
				continue
			}
			r2.fail("s3 backend, yet a blob is missing: %s", desc(o))
		}
		if o.Op == "get" && o.OK && !o.Absent {
			nGets++
			nBytes += o.Size
		}
	}

	// ---- R1: per-key register ----
	byKey := map[string][]r2Op{}
	for _, o := range in.ops {
		if o.Key != "" {
			byKey[o.Key] = append(byKey[o.Key], o)
		}
	}
	stale := 0
	excused := map[string]int{}
	var maxExcuse time.Duration
	flag := func(rd r2Op, format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		ok, inOut, why := stalenessExcuse(outs, rd.Host, rd.Sent, fenced)
		switch {
		case rd.Final:
			r2.fail("at quiescence, %s: %s", msg, desc(rd))
		case ok:
			excused[why]++
			for _, o := range outs {
				if (o.host == "" || o.host == rd.Host) && !rd.Sent.Before(o.start) && !rd.Sent.After(o.end.Add(staleAfter)) && rd.Sent.Sub(o.start) > maxExcuse {
					maxExcuse = rd.Sent.Sub(o.start)
				}
			}
		case inOut:
			r4.fail("stale read past the documented bound (%s): %s: %s", why, msg, desc(rd))
		default:
			stale++
			r1.fail("%s: %s", msg, desc(rd))
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	nReads := 0
	for _, k := range keys {
		ops := byKey[k]
		vals := map[string]*r2w{}
		var acked, dels []*r2w
		for _, o := range ops {
			if (o.Op != "put" && o.Op != "mpu-complete" && o.Op != "del") || (o.Op == "put" && o.BadMD5) {
				continue
			}
			if !o.OK && o.Status >= 400 && o.Status < 500 {
				continue // refused before it wrote anything (bad request)
			}
			w := &r2w{o: o, del: o.Op == "del", sent: o.Sent, recv: never}
			if o.OK {
				w.recv = o.Recv
				acked = append(acked, w)
			}
			if w.del {
				dels = append(dels, w)
			} else {
				vals[o.Val] = w
			}
		}
		var reads []r2Op
		for _, o := range ops {
			if (o.Op == "get" || o.Op == "head") && o.OK && o.Bad == "" {
				reads = append(reads, o)
			}
		}
		sort.Slice(reads, func(i, j int) bool { return reads[i].Sent.Before(reads[j].Sent) })
		nReads += len(reads)
		// deleted reports whether some delete can take effect after
		// write w and before the read rd ends.
		deleted := func(w *r2w, rd r2Op) bool {
			for _, d := range dels {
				if d.sent.Before(rd.Recv) && d.recv.After(w.sent) {
					return true
				}
			}
			return false
		}
		for i, rd := range reads {
			if rd.Absent {
				for _, w := range acked {
					if !w.del && w.recv.Before(rd.Sent) && !deleted(w, rd) {
						flag(rd, "absent, yet %s was acknowledged at %s and no delete could follow it", w.o.ID, w.recv.Format("15:04:05.000"))
						break
					}
				}
			} else {
				w := vals[rd.Val]
				switch {
				case w == nil && valKey[rd.Val] != "" && valKey[rd.Val] != k:
					r1.fail("read %s, a value of key %s: %s", rd.Val, valKey[rd.Val], desc(rd))
					continue
				case w == nil:
					r1.fail("read %s, which no put or complete of %s wrote: %s", rd.Val, k, desc(rd))
					continue
				case w.sent.After(rd.Recv):
					r1.fail("read %s, written only after the read ended: %s", rd.Val, desc(rd))
					continue
				}
				for _, x := range acked {
					if x != w && x.sent.After(w.recv) && x.recv.Before(rd.Sent) {
						flag(rd, "read %s, which %s (%s, acknowledged %s) had replaced before the read was sent", rd.Val, x.o.ID, x.o.Op, x.recv.Format("15:04:05.000"))
						break
					}
				}
			}
			// Monotonic: nothing older than an earlier read saw.
			for _, prev := range reads[:i] {
				if !prev.Recv.Before(rd.Sent) || prev.Absent || prev.Val == rd.Val {
					continue
				}
				pw := vals[prev.Val]
				if pw == nil {
					continue
				}
				if rd.Absent {
					if !deleted(pw, rd) {
						flag(rd, "absent after %s read %s at %s, and no delete could follow that write", prev.ID, prev.Val, prev.Recv.Format("15:04:05.000"))
						break
					}
				} else if w := vals[rd.Val]; w != nil && w.recv.Before(pw.sent) {
					flag(rd, "read %s after %s had read the newer %s at %s", rd.Val, prev.ID, prev.Val, prev.Recv.Format("15:04:05.000"))
					break
				}
			}
		}
	}
	r1.Summary = fmt.Sprintf("%d keys, %d reads checked; %d stale outside a declared outage", len(keys), nReads, stale)

	// ---- R3: no acknowledged write past the fence or while mgmt is unreachable ----
	nW := 0
	for _, o := range in.ops {
		if !isWrite(o.Op) || !o.OK || (o.Op == "put" && o.BadMD5) {
			continue
		}
		nW++
		for _, ot := range outs {
			if ot.host != "" && ot.host != o.Host {
				continue
			}
			from, to := ot.start.Add(300*time.Millisecond), ot.end
			if ot.kind == "restart-mgmt" {
				if !ot.crash || ot.down <= 0 {
					continue // SIGTERM: mgmt may still answer while it shuts down
				}
				to = ot.start.Add(ot.down)
			}
			if o.Sent.After(from) && o.Recv.Before(to) {
				r3.fail("acknowledged while mgmt was unreachable (%s %s→%s): %s", ot.kind, ot.start.Format("15:04:05.000"), to.Format("15:04:05.000"), desc(o))
			}
		}
		if fenced && len(in.deps) > 0 {
			// The lease holder when it was sent: the latest deployment
			// change before then (or a change to this host just after,
			// which the watcher saw late).
			var cur depEv
			for _, d := range in.deps {
				if !d.At.After(o.Sent) {
					cur = d
				}
			}
			late := false
			for _, d := range in.deps {
				if d.At.After(o.Sent) && d.At.Before(o.Recv.Add(2*time.Second)) && d.Host == o.Host {
					late = true
				}
			}
			if cur.Host != "" && cur.Host != o.Host && !late {
				r3.fail("acknowledged by %s after the lease moved to %s (epoch %d at %s): %s", o.Host, cur.Host, cur.Epoch, cur.At.Format("15:04:05.000"), desc(o))
			}
		}
	}
	r3.Summary = fmt.Sprintf("%d acknowledged writes, %d declared mgmt outages", nW, len(outs))

	// ---- R4: stale reads only inside an outage and the documented bound ----
	if fenced {
		for _, o := range in.ops {
			if !o.OK || o.Op == "list" && o.Final {
				continue
			}
			for _, ot := range outs {
				if ot.host == "" || ot.host != o.Host {
					continue
				}
				if o.Sent.Sub(ot.start) > fenceBound && o.Recv.Before(ot.end) {
					r4.fail("answered %.0fs into %s of %s: a fenced workload stops 30 s after losing mgmt: %s", o.Sent.Sub(ot.start).Seconds(), ot.kind, ot.host, desc(o))
				}
			}
		}
	}
	var ex []string
	for k, n := range excused {
		ex = append(ex, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(ex)
	r4.Summary = fmt.Sprintf("stale reads inside declared outages: %v (latest %.1fs into one); r2_blob_missing from a stale index during an outage (s3): %d", ex, maxExcuse.Seconds(), staleMissing)
	var slow []string
	for _, o := range in.ops {
		if (o.Op == "get" || o.Op == "head") && o.OK {
			if _, inOut, _ := stalenessExcuse(outs, o.Host, o.Sent, false); inOut && o.Recv.Sub(o.Sent) > 3*time.Second {
				slow = append(slow, fmt.Sprintf("%s %.1fs", o.ID, o.Recv.Sub(o.Sent).Seconds()))
			}
		}
	}
	if len(slow) > 0 {
		r4.note("reads that took over 3 s during an outage (the documented wait is 2 s): %d, e.g. %v", len(slow), slow[:min(len(slow), 5)])
	}

	// ---- R2 (durability) and R5 (list agrees with the index) at quiescence ----
	type fin struct{ head, get *r2Op }
	finals := map[string]map[string]*fin{} // dns → key → its final head and get
	lists := map[string]r2Op{}
	for i := range in.ops {
		o := &in.ops[i]
		if !o.Final {
			continue
		}
		if o.Op == "list" {
			lists[o.DNS] = *o
			continue
		}
		if finals[o.DNS] == nil {
			finals[o.DNS] = map[string]*fin{}
		}
		f := finals[o.DNS][o.Key]
		if f == nil {
			f = &fin{}
			finals[o.DNS][o.Key] = f
		}
		if o.Op == "head" {
			f.head = o
		} else {
			f.get = o
		}
	}
	referenced := 0
	dnss := make([]string, 0, len(finals))
	for d := range finals {
		dnss = append(dnss, d)
	}
	sort.Strings(dnss)
	for di, dns := range dnss {
		host := r2HostOf(dns)
		present := map[string]bool{}
		for k, f := range finals[dns] {
			if f.head == nil || f.get == nil {
				continue
			}
			if !f.head.OK {
				r2.fail("at quiescence the index can't be read (head): %s", desc(*f.head))
				continue
			}
			if f.head.Absent {
				if f.get.OK && !f.get.Absent {
					r5.fail("head says %s is absent, get returns %s: %s", k, f.get.Val, desc(*f.get))
				}
				continue
			}
			present[k] = true
			if di == 0 {
				referenced++
				if strings.HasPrefix(k, "m") {
					referenced++
				}
			}
			v := f.head.Val
			writers := map[string]bool{}
			for _, o := range in.ops {
				if o.Val == v && o.Op == "put" {
					writers[o.Host] = true
				}
			}
			for hh := range partHosts[v] {
				writers[hh] = true
			}
			local := len(writers) == 1 && writers[host]
			switch {
			case f.get.OK && !f.get.Absent && f.get.Val != v:
				r5.fail("head names %s, get returns %s: %s", v, f.get.Val, desc(*f.get))
			case f.get.OK && f.get.Absent:
				r5.fail("head names %s, get says absent: %s", v, desc(*f.get))
			case f.get.OK:
			case in.backend == "s3":
				r2.fail("at quiescence, s3: get of %s (%s) fails: %s", k, v, desc(*f.get))
			case !strings.Contains(f.get.Err, "r2_blob_missing") && !strings.HasPrefix(k, "m"):
				r2.fail("fs: get of %s (%s) fails with something other than r2_blob_missing: %s", k, v, desc(*f.get))
			case local:
				r2.fail("fs: %s was written on %s, which serves it now, yet get fails: %s", v, host, desc(*f.get))
			default:
				r2.note("fs: %s of %s was written on %v, read on %s: %s (as documented)", v, k, r2Sorted(writers), host, trunc(f.get.Err, 80))
			}
		}
		if l, ok := lists[dns]; !ok || !l.OK {
			r5.fail("no list at quiescence from %s: %+v", dns, l.Err)
		} else {
			listed := map[string]bool{}
			for _, k := range l.Keys {
				listed[k] = true
			}
			if a, b := strings.Join(r2Sorted(listed), ","), strings.Join(r2Sorted(present), ","); a != b {
				r5.fail("list on %s has [%s], the index (head) has [%s]", host, a, b)
			}
		}
	}
	if in.blobErr != nil {
		r5.note("blob count: %v", in.blobErr)
	} else {
		r5.note("the backend holds %d blobs; the index at the end references %d; the rest (%d) are orphans or parts of unfinished uploads", in.blobs, referenced, in.blobs-referenced)
	}
	r5.Summary = fmt.Sprintf("%d endpoints listed at quiescence; %d blobs in the backend, %d referenced", len(lists), in.blobs, referenced)
	r2.Summary = fmt.Sprintf("%d object reads, %d bytes checked; backend %s", nGets, nBytes, in.backend)

	// ---- R6: multipart: whole objects, uploads kept until aborted ----
	created := map[string]r2Op{}
	aborted := map[string]time.Time{}
	nUp := 0
	for _, o := range in.ops {
		switch o.Op {
		case "mpu-create":
			if o.OK {
				created[o.Upload] = o
				nUp++
			}
		case "mpu-abort":
			aborted[o.Upload] = o.Sent
		}
	}
	for _, o := range in.ops {
		if (o.Op != "mpu-part" && o.Op != "mpu-complete") || o.OK || !strings.Contains(o.Err, "multipart upload does not exist") {
			continue
		}
		cr, ok := created[o.Upload]
		if !ok {
			continue
		}
		if at, ab := aborted[o.Upload]; ab && at.Before(o.Recv) {
			continue
		}
		if ok, _, _ := stalenessExcuse(outs, o.Host, o.Sent, false); ok {
			r6.note("upload %s unknown to %s during an outage: %s", cr.Upload, o.Host, desc(o))
			continue
		}
		r6.fail("upload %s (created %s by %s) was never aborted, yet: %s", trunc(cr.Upload, 12), cr.Sent.Format("15:04:05.000"), cr.ID, desc(o))
	}
	r6.Summary = fmt.Sprintf("%d uploads created", nUp)

	// Availability (reported, not gated).
	tot, ok := 0, 0
	for _, o := range in.ops {
		if o.Final || o.Probe || o.Client == "nemesis" || inFault(in.faults, o.Sent, 30*time.Second) {
			continue
		}
		tot++
		if o.OK {
			ok++
		}
	}
	if tot > 0 {
		r1.note("availability outside faults (+30 s): %d of %d ops ok (%.1f%%)", ok, tot, 100*float64(ok)/float64(tot))
	}
	return []result{r1, r2, r3, r4, r5, r6}
}
