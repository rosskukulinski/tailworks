package mgmtpg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// FakeSetec is the custody the replicas share (tw-mgmt -custody setec
// -setec-url URL): put, activate, get and delete, one version per name.
type FakeSetec struct {
	URL  string
	mu   sync.Mutex
	vals map[string][]byte
}

// NewFakeSetec serves an empty store until the test ends.
func NewFakeSetec(t testing.TB) *FakeSetec {
	f := &FakeSetec{vals: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *FakeSetec) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Sec-X-Tailscale-No-Browsers") != "setec" {
		w.WriteHeader(403)
		return
	}
	var req struct {
		Name  string
		Value []byte
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/put":
		f.vals[req.Name] = req.Value
		json.NewEncoder(w).Encode(1)
	case "/api/activate":
	case "/api/get":
		v, ok := f.vals[req.Name]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Value": v, "Version": 1})
	case "/api/delete":
		delete(f.vals, req.Name)
	default:
		w.WriteHeader(500)
	}
}

// Delete removes one entry.
func (f *FakeSetec) Delete(name string) {
	f.mu.Lock()
	delete(f.vals, name)
	f.mu.Unlock()
}

// DeletePrefix removes every entry whose name starts with prefix.
func (f *FakeSetec) DeletePrefix(prefix string) {
	f.mu.Lock()
	for k := range f.vals {
		if strings.HasPrefix(k, prefix) {
			delete(f.vals, k)
		}
	}
	f.mu.Unlock()
}
