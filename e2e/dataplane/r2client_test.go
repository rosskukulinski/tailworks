//go:build e2edataplane

package dataplane

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The R2 workload's client side (ledger/r2.md). Every value is the bytes
// gen(<op ID>|, size) (multipart: one gen(<op ID>#<n>|, size) per part),
// so a read names the write it saw and the client can check every byte
// and the etag.

// r2Op is one client operation on the r2 workload.
type r2Op struct {
	ID     string
	Client string
	Op     string // put | get | head | del | list | mpu-create | mpu-part | mpu-complete | mpu-abort
	Key    string `json:",omitempty"`
	// Val: for put and mpu-complete the value written (its op ID); for get
	// and head the value read ("" with Absent).
	Val    string   `json:",omitempty"`
	Size   int      `json:",omitempty"`
	Etag   string   `json:",omitempty"` // put: the etag expected; get/head: the one answered
	Upload string   `json:",omitempty"`
	Part   int      `json:",omitempty"`
	BadMD5 bool     `json:",omitempty"` // a put sent with a wrong MD5: it must fail
	Keys   []string `json:",omitempty"` // list
	Sent   time.Time
	Recv   time.Time
	OK     bool
	Absent bool   `json:",omitempty"`
	Status int    `json:",omitempty"`
	Err    string `json:",omitempty"`
	DNS    string
	Host   string
	Epoch  int64  `json:",omitempty"`
	Bad    string `json:",omitempty"` // the client's own check failed: wrong bytes, etag, size
	Probe  bool   `json:",omitempty"` // sent to an endpoint that is not (or no longer) current
	Final  bool   `json:",omitempty"` // a read at quiescence
}

// r2Hist holds the R2 ops (they also go to history.jsonl as "r2").
type r2Hist struct {
	mu  sync.Mutex
	ops []r2Op
	h   *history
}

func (rh *r2Hist) add(o r2Op) {
	rh.mu.Lock()
	rh.ops = append(rh.ops, o)
	rh.mu.Unlock()
	rh.h.mu.Lock()
	rh.h.log("r2", o)
	rh.h.mu.Unlock()
}

func (rh *r2Hist) all() []r2Op {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	return append([]r2Op(nil), rh.ops...)
}

// gen is prefix repeated to size bytes (testdata/r2/src/index.js has the same).
func gen(prefix string, size int) []byte {
	out := make([]byte, size)
	for i := 0; i < size; i += len(prefix) {
		copy(out[i:], prefix)
	}
	return out
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

const (
	r2PartSize  = 5 << 20 // R2's minimum part (all but the last)
	r2Keys      = 4       // k0..k3: the register clients' keys
	r2Sentinels = 3       // s0..s2: written once at the start
)

var r2MpuKeys = []string{"m0", "m1"}

// mpuParts is the parts of multipart value v: sizes and bytes of each.
func mpuBody(v string, last int) []byte {
	return append(gen(v+"#1|", r2PartSize), gen(v+"#2|", last)...)
}

// mpuEtag is R2's multipart etag: MD5 of the parts' MD5s, "-<parts>".
func mpuEtag(v string, last int) string {
	a := md5.Sum(gen(v+"#1|", r2PartSize))
	b := md5.Sum(gen(v+"#2|", last))
	s := md5.Sum(append(a[:], b[:]...))
	return hex.EncodeToString(s[:]) + "-2"
}

// valueOf names the write whose bytes b are, and checks every byte:
// "" and a reason if b is not exactly some value's bytes.
func valueOf(b []byte) (v string, multipart bool, bad string) {
	i := bytes.IndexAny(b, "|#")
	if i <= 0 {
		return "", false, fmt.Sprintf("%d bytes that name no write: %q", len(b), trunc(string(b), 40))
	}
	v = string(b[:i])
	if b[i] == '|' {
		if !bytes.Equal(b, gen(v+"|", len(b))) {
			return v, false, fmt.Sprintf("bytes of %s are not all its own (%d bytes)", v, len(b))
		}
		return v, false, ""
	}
	if len(b) <= r2PartSize || !bytes.Equal(b, mpuBody(v, len(b)-r2PartSize)) {
		return v, true, fmt.Sprintf("multipart %s torn or wrong: %d bytes, not part 1 (%d) + part 2 of %s", v, len(b), r2PartSize, v)
	}
	return v, true, ""
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// r2Workload runs the clients.
type r2Workload struct {
	rh      *r2Hist
	cli     *http.Client // small ops
	big     *http.Client // multipart parts and their reads
	current func() []string
	seen    func() map[string]string // every endpoint DNS seen → host
	stop    chan struct{}
	wg      sync.WaitGroup
	seqMu   sync.Mutex
	seq     int
}

func (w *r2Workload) id(prefix string) string {
	w.seqMu.Lock()
	defer w.seqMu.Unlock()
	w.seq++
	return fmt.Sprintf("%s%06d", prefix, w.seq)
}

func (w *r2Workload) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

func r2HostOf(dns string) string {
	if i := strings.Index(dns, "-"); i > 0 {
		return dns[:i]
	}
	return ""
}

// call does one request to dns and fills o's outcome. ok: the status that
// counts as success besides 200 (404 for an absent key).
func (w *r2Workload) call(hc *http.Client, o *r2Op, method, dns, path string, body []byte, hdr map[string]string) []byte {
	o.DNS, o.Host = dns, r2HostOf(dns)
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	rq, _ := http.NewRequest(method, "http://"+dns+path, rd)
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	o.Sent = time.Now()
	resp, err := hc.Do(rq)
	if err != nil {
		o.Recv, o.Err = time.Now(), err.Error()
		return nil
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	o.Recv, o.Status = time.Now(), resp.StatusCode
	o.Epoch, _ = strconv.ParseInt(resp.Header.Get("x-epoch"), 10, 64)
	if err != nil {
		o.Err = "reading the answer: " + err.Error()
		return nil
	}
	switch {
	case resp.StatusCode == 200:
		o.OK = true
		if o.Op == "get" {
			o.Etag = resp.Header.Get("x-etag")
			if sz := resp.Header.Get("x-size"); sz != strconv.Itoa(len(b)) {
				o.Bad = fmt.Sprintf("size %s answered with %d bytes", sz, len(b))
			}
		}
	case resp.StatusCode == 404 && bytes.Contains(b, []byte(`"absent"`)):
		o.OK, o.Absent = true, true
	default:
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = trunc(strings.TrimSpace(string(b)), 200)
		}
		o.Err = fmt.Sprintf("%d %s", resp.StatusCode, e.Error)
	}
	return b
}

// get reads key from dns and checks the bytes against the etag.
func (w *r2Workload) get(hc *http.Client, client, dns, key string) r2Op {
	o := r2Op{ID: w.id("g"), Client: client, Op: "get", Key: key}
	b := w.call(hc, &o, "GET", dns, "/o?k="+url.QueryEscape(key), nil, nil)
	if o.OK && !o.Absent {
		v, mp, bad := valueOf(b)
		o.Val, o.Size = v, len(b)
		switch {
		case bad != "":
			o.Bad = bad
		case !mp && o.Etag != md5hex(b):
			o.Bad = fmt.Sprintf("etag %s, the bytes' MD5 is %s", o.Etag, md5hex(b))
		case mp && o.Etag != mpuEtag(v, len(b)-r2PartSize):
			o.Bad = fmt.Sprintf("multipart etag %s, want %s", o.Etag, mpuEtag(v, len(b)-r2PartSize))
		}
	}
	return o
}

func (w *r2Workload) head(client, dns, key string) r2Op {
	o := r2Op{ID: w.id("h"), Client: client, Op: "head", Key: key}
	b := w.call(w.cli, &o, "GET", dns, "/h?k="+url.QueryEscape(key), nil, nil)
	if o.OK && !o.Absent {
		var r struct {
			Etag string
			Size int
		}
		json.Unmarshal(b, &r)
		o.Etag, o.Size = r.Etag, r.Size
	}
	return o
}

func (w *r2Workload) put(hc *http.Client, client, dns, key string, size int, md5mode int) r2Op {
	o := r2Op{ID: w.id("p"), Client: client, Op: "put", Key: key, Size: size}
	o.Val = o.ID
	body := gen(o.Val+"|", size)
	o.Etag = md5hex(body)
	hdr := map[string]string{}
	switch md5mode {
	case 1:
		s := md5.Sum(body)
		hdr["x-md5"] = hex.EncodeToString(s[:])
	case 2:
		s := md5.Sum(append(body, 'x'))
		hdr["x-md5"] = hex.EncodeToString(s[:])
		o.BadMD5 = true
	}
	b := w.call(hc, &o, "PUT", dns, "/o?k="+url.QueryEscape(key), body, hdr)
	if o.OK {
		var r struct{ Etag string }
		json.Unmarshal(b, &r)
		if r.Etag != o.Etag {
			o.Bad = fmt.Sprintf("put answered etag %s, the bytes' MD5 is %s", r.Etag, o.Etag)
		}
	}
	return o
}

func (w *r2Workload) del(client, dns, key string) r2Op {
	o := r2Op{ID: w.id("d"), Client: client, Op: "del", Key: key}
	w.call(w.cli, &o, "DELETE", dns, "/o?k="+url.QueryEscape(key), nil, nil)
	return o
}

func (w *r2Workload) list(client, dns string) r2Op {
	o := r2Op{ID: w.id("l"), Client: client, Op: "list"}
	b := w.call(w.cli, &o, "GET", dns, "/list", nil, nil)
	if o.OK {
		var r struct{ Keys []struct{ K string } }
		json.Unmarshal(b, &r)
		for _, k := range r.Keys {
			o.Keys = append(o.Keys, k.K)
		}
		sort.Strings(o.Keys)
	}
	return o
}

// pick is a current endpoint (several with replicas).
func (w *r2Workload) pick(rnd *rand.Rand) string {
	cur := w.current()
	if len(cur) == 0 {
		return ""
	}
	return cur[rnd.Intn(len(cur))]
}

// stale is an endpoint seen before that is not current now, or "".
func (w *r2Workload) stale(rnd *rand.Rand) string {
	cur := map[string]bool{}
	for _, d := range w.current() {
		cur[d] = true
	}
	var old []string
	for d := range w.seen() {
		if !cur[d] {
			old = append(old, d)
		}
	}
	sort.Strings(old)
	if len(old) == 0 {
		return ""
	}
	return old[rnd.Intn(len(old))]
}

func (w *r2Workload) loop(f func()) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for !w.stopped() {
			f()
		}
	}()
}

func (w *r2Workload) pause(d time.Duration) {
	select {
	case <-w.stop:
	case <-time.After(d):
	}
}

// run starts the clients: four register clients on k0..k3, a multipart
// client on m0/m1 and its reader, and probes of stale endpoints.
func (w *r2Workload) run(seed int64) {
	for c := 0; c < 4; c++ {
		rnd := rand.New(rand.NewSource(seed*100 + int64(c)))
		name := fmt.Sprintf("c%d", c)
		w.loop(func() {
			dns := w.pick(rnd)
			if dns == "" {
				w.pause(200 * time.Millisecond)
				return
			}
			key := fmt.Sprintf("k%d", rnd.Intn(r2Keys))
			var o r2Op
			switch x := rnd.Intn(100); {
			case x < 40:
				mode := 0
				switch rnd.Intn(20) {
				case 0, 1:
					mode = 1
				case 2:
					mode = 2
				}
				o = w.put(w.cli, name, dns, key, 64+rnd.Intn(16<<10), mode)
			case x < 75:
				o = w.get(w.cli, name, dns, key)
			case x < 85:
				o = w.head(name, dns, key)
			case x < 95:
				o = w.del(name, dns, key)
			default:
				o = w.list(name, dns)
			}
			w.rh.add(o)
			w.pause(time.Duration(50+rnd.Intn(100)) * time.Millisecond)
		})
	}
	// Multipart: create, part 1 (5 MiB), part 2, a wait (so uploads span
	// faults: they are kept until aborted), then complete or abort.
	mrnd := rand.New(rand.NewSource(seed*100 + 50))
	w.loop(func() {
		dns := w.pick(mrnd)
		if dns == "" {
			w.pause(200 * time.Millisecond)
			return
		}
		key := r2MpuKeys[mrnd.Intn(len(r2MpuKeys))]
		v := w.id("u")
		last := 1 + mrnd.Intn(64<<10)
		cr := r2Op{ID: v + "-c", Client: "mpu", Op: "mpu-create", Key: key, Val: v}
		b := w.call(w.cli, &cr, "POST", dns, "/mpu/create?k="+key, nil, nil)
		if cr.OK {
			var r struct{ UploadID string }
			json.Unmarshal(b, &r)
			cr.Upload = r.UploadID
		}
		w.rh.add(cr)
		if !cr.OK {
			w.pause(time.Second)
			return
		}
		var parts []map[string]any
		for n, size := range []int{r2PartSize, last} {
			p := r2Op{ID: fmt.Sprintf("%s-p%d", v, n+1), Client: "mpu", Op: "mpu-part", Key: key, Val: v, Upload: cr.Upload, Part: n + 1, Size: size}
			b := w.call(w.big, &p, "POST", w.pick(mrnd), fmt.Sprintf("/mpu/part?k=%s&u=%s&n=%d&v=%s&size=%d", key, url.QueryEscape(cr.Upload), n+1, v, size), nil, nil)
			if p.OK {
				var r struct{ Etag string }
				json.Unmarshal(b, &r)
				p.Etag = r.Etag
				if want := md5hex(gen(fmt.Sprintf("%s#%d|", v, n+1), size)); r.Etag != want {
					p.Bad = fmt.Sprintf("part etag %s, want %s", r.Etag, want)
				}
				parts = append(parts, map[string]any{"partNumber": n + 1, "etag": r.Etag})
			}
			w.rh.add(p)
			if !p.OK {
				break
			}
		}
		w.pause(time.Duration(mrnd.Intn(20000)) * time.Millisecond)
		if len(parts) < 2 || mrnd.Intn(5) == 0 {
			a := r2Op{ID: v + "-a", Client: "mpu", Op: "mpu-abort", Key: key, Val: v, Upload: cr.Upload}
			w.call(w.cli, &a, "POST", w.pick(mrnd), "/mpu/abort?k="+key+"&u="+url.QueryEscape(cr.Upload), nil, nil)
			w.rh.add(a)
		} else {
			body, _ := json.Marshal(parts)
			c := r2Op{ID: v, Client: "mpu", Op: "mpu-complete", Key: key, Val: v, Upload: cr.Upload, Size: r2PartSize + last, Etag: mpuEtag(v, last)}
			rb := w.call(w.cli, &c, "POST", w.pick(mrnd), "/mpu/complete?k="+key+"&u="+url.QueryEscape(cr.Upload), body, nil)
			if c.OK {
				var r struct{ Etag string }
				json.Unmarshal(rb, &r)
				if r.Etag != c.Etag {
					c.Bad = fmt.Sprintf("complete answered etag %s, want %s", r.Etag, c.Etag)
				}
			}
			w.rh.add(c)
		}
		w.pause(time.Second)
	})
	// The multipart keys' reader: whole objects only (R6).
	rrnd := rand.New(rand.NewSource(seed*100 + 60))
	w.loop(func() {
		if dns := w.pick(rrnd); dns != "" {
			w.rh.add(w.get(w.big, "mread", dns, r2MpuKeys[rrnd.Intn(len(r2MpuKeys))]))
		}
		w.pause(1500 * time.Millisecond)
	})
	// Probes: reads and writes sent to endpoints that are no longer
	// current, so a copy that should be fenced is caught answering.
	prnd := rand.New(rand.NewSource(seed*100 + 70))
	w.loop(func() {
		if dns := w.stale(prnd); dns != "" {
			key := fmt.Sprintf("k%d", prnd.Intn(r2Keys))
			var o r2Op
			switch prnd.Intn(3) {
			case 0:
				o = w.get(w.cli, "probe", dns, key)
			case 1:
				o = w.put(w.cli, "probe", dns, key, 64+prnd.Intn(1024), 0)
			default:
				o = w.del("probe", dns, key)
			}
			o.Probe = true
			w.rh.add(o)
		}
		w.pause(time.Second)
	})
}
