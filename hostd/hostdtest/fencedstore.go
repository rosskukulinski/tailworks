package hostdtest

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// FencedStore is the example external system of docs/locks.md: a service
// outside tailworks that the lock protects. It remembers, per resource, the
// highest fencing token it has accepted and refuses any write that carries
// a lower one (409 stale_token). Equal tokens are the same holder writing
// again and are accepted.
type FencedStore struct {
	mu     sync.Mutex
	max    map[string]int64
	Writes []FencedWrite
}

// FencedWrite is one write the store saw (accepted or refused).
type FencedWrite struct {
	Resource string
	Token    int64
	Caller   string // the writer's tailnet node (WhoIs)
	Accepted bool
	At       time.Time
}

// ServeFencedStore serves the store on n:80: POST /write?resource=<r> with
// header Fencing-Token: <n>.
func ServeFencedStore(n *nettest.Node) (*FencedStore, error) {
	fs := &FencedStore{max: map[string]int64{}}
	ln, err := n.Listen("tcp", ":80")
	if err != nil {
		return nil, err
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("content-type", "application/json")
		res := r.URL.Query().Get("resource")
		tok, err := strconv.ParseInt(r.Header.Get("Fencing-Token"), 10, 64)
		if r.URL.Path != "/write" || r.Method != "POST" || res == "" || err != nil || tok <= 0 {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": "bad_request", "detail": "POST /write?resource=<r> with Fencing-Token: <n>"})
			return
		}
		caller := ""
		if who, err := n.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil {
			caller = who.Node.Name
		}
		fs.mu.Lock()
		cur := fs.max[res]
		ok := tok >= cur
		if ok {
			fs.max[res] = tok
		}
		fs.Writes = append(fs.Writes, FencedWrite{Resource: res, Token: tok, Caller: caller, Accepted: ok, At: time.Now()})
		fs.mu.Unlock()
		if !ok {
			w.WriteHeader(409)
			json.NewEncoder(w).Encode(map[string]any{"error": "stale_token", "token": tok, "max": cur})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "token": tok})
	}))
	return fs, nil
}

// Max is the highest token accepted for resource.
func (fs *FencedStore) Max(resource string) int64 {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.max[resource]
}

// Snapshot copies the writes seen so far.
func (fs *FencedStore) Snapshot() []FencedWrite {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]FencedWrite(nil), fs.Writes...)
}
