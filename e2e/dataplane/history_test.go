//go:build e2edataplane

package dataplane

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// The op history. Client-side ops are timed by the test's clock (Sent
// before the request leaves, Recv when the answer is in); deliveries and
// pulses by the recorder node's receipt time. Every event is also appended
// to history.jsonl as it happens.

type sendOp struct {
	K          string
	Sent, Recv time.Time
	OK         bool
	Err        string `json:",omitempty"`
}

type delivery struct {
	Q, K, ID string
	Attempts int
	At       time.Time
	Node     string // the consumer's tailnet node (WhoIs)
}

type regOp struct {
	Op         string // incr | get | probe
	Sent, Recv time.Time
	OK         bool
	V          int
	Epoch      int64
	Host       string // the host whose endpoint was called
	Err        string `json:",omitempty"`
}

type d1Op struct {
	ID         string
	Sent, Recv time.Time
	OK         bool
	Err        string `json:",omitempty"`
}

type pulseEv struct {
	At    time.Time
	Node  string
	Epoch int64
}

type lockEv struct {
	Client     string
	Kind       string // grant | renew | lost | release | write
	Token      int64
	Epoch      int64
	Host       string
	Sent, Recv time.Time
	Status     int
	Err        string `json:",omitempty"`
}

type faultEv struct {
	Kind       string
	Host       string
	Start, End time.Time
	Detail     string `json:",omitempty"`
}

type depEv struct {
	At    time.Time
	Host  string
	Epoch int64
	DNS   string
}

type history struct {
	mu     sync.Mutex
	f      *os.File
	enc    *json.Encoder
	sends  []sendOp
	dels   []delivery
	regs   []regOp
	d1s    []d1Op
	kvs    []kvOp // kv_test.go
	pulses []pulseEv
	locks  []lockEv
	faults []faultEv
	deps   []depEv
}

func newHistory(path string) (*history, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &history{f: f, enc: json.NewEncoder(f)}, nil
}

func (h *history) close() { h.f.Close() }

func (h *history) log(typ string, v any) {
	h.enc.Encode(map[string]any{"type": typ, "ev": v})
}

func (h *history) send(o sendOp) {
	h.mu.Lock()
	h.sends = append(h.sends, o)
	h.log("send", o)
	h.mu.Unlock()
}
func (h *history) del(d delivery) {
	h.mu.Lock()
	h.dels = append(h.dels, d)
	h.log("delivery", d)
	h.mu.Unlock()
}
func (h *history) reg(o regOp) {
	h.mu.Lock()
	h.regs = append(h.regs, o)
	h.log("reg", o)
	h.mu.Unlock()
}
func (h *history) d1(o d1Op) { h.mu.Lock(); h.d1s = append(h.d1s, o); h.log("d1", o); h.mu.Unlock() }
func (h *history) pulse(p pulseEv) {
	h.mu.Lock()
	h.pulses = append(h.pulses, p)
	h.log("pulse", p)
	h.mu.Unlock()
}
func (h *history) lock(e lockEv) {
	h.mu.Lock()
	h.locks = append(h.locks, e)
	h.log("lock", e)
	h.mu.Unlock()
}
func (h *history) fault(f faultEv) {
	h.mu.Lock()
	h.faults = append(h.faults, f)
	h.log("fault", f)
	h.mu.Unlock()
}
func (h *history) dep(d depEv) {
	h.mu.Lock()
	h.deps = append(h.deps, d)
	h.log("deployment", d)
	h.mu.Unlock()
}
