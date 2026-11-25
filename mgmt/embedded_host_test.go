package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newEmbeddedTestMgmt is a bare Mgmt (SQLite store, file custody, no API).
func newEmbeddedTestMgmt(t *testing.T) *Mgmt {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore(context.Background(), filepath.Join(dir, "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cust, err := OpenFileCustody(filepath.Join(dir, "custody"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Store: st, Custody: cust, IDSecret: bytes.Repeat([]byte{3}, 32), ReapInterval: time.Hour, PushTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

// An embedded host (single-process tw up) registers with push URL
// inproc:// only when the context carries the marker; its pushes are then
// served through the handler SetInProcessPush registered, nothing dialled.
func TestEmbeddedHostRegistrationAndPush(t *testing.T) {
	t.Parallel()
	m := newEmbeddedTestMgmt(t)
	ctx := context.Background()
	req := RegisterRequest{HostID: "h1", CapacityNodes: 5, Tiers: []string{"process"}, PushURL: EmbeddedPushURL}

	// Without the marker (a local-dev or tsnet caller): refused, nothing stored.
	if _, _, err := m.RegisterHost(ctx, req, "", ""); err == nil || asAPIError(err).Code != "host_identity_mismatch" {
		t.Fatalf("inproc:// without the marker: %v", err)
	}
	if _, _, err := m.RegisterHost(WithEmbeddedHost(ctx, "h1"), req, "nNODE", "100.64.0.5"); err == nil || asAPIError(err).Code != "host_identity_mismatch" {
		t.Fatalf("inproc:// from a bound node: %v", err)
	}
	if _, _, err := m.RegisterHost(WithEmbeddedHost(ctx, "other"), req, "", ""); err == nil || asAPIError(err).Code != "host_identity_mismatch" {
		t.Fatalf("inproc:// with another host's marker: %v", err)
	}
	if _, err := m.store.GetHost(ctx, "h1"); err == nil {
		t.Fatal("a refused embedded registration created the host record")
	}

	// With it: created, no node, no secret needed, push target inproc://.
	ectx := WithEmbeddedHost(ctx, "h1")
	h, created, err := m.RegisterHost(ectx, req, "", "")
	if err != nil || !created || h.NodeID != "" || h.PushPort != 0 || h.RegistrationSecretHash != "" {
		t.Fatalf("embedded register: %+v created=%v err=%v", h, created, err)
	}
	if m.pushTarget("h1") != EmbeddedPushURL {
		t.Fatalf("push target %q", m.pushTarget("h1"))
	}
	// A record bound to a tailnet node is not taken over by an embedded host.
	bound := Host{ID: "h2", NodeID: "nREMOTE", Tiers: []string{"process"}, CapacityNodes: 1, LastSeen: 1, CreatedAt: 1, UpdatedAt: 1}
	if err := m.store.UpsertHost(ctx, bound); err != nil {
		t.Fatal(err)
	}
	req2 := req
	req2.HostID = "h2"
	if _, _, err := m.RegisterHost(WithEmbeddedHost(ctx, "h2"), req2, "", ""); err == nil || asAPIError(err).Code != "host_identity_mismatch" {
		t.Fatalf("embedded takeover of a node-bound host: %v", err)
	}

	// Pushes: no handler yet → error (nothing to dial either).
	if err := m.push("h1", "/v1/push/kill", map[string]any{"objectID": "o1"}); err == nil {
		t.Fatal("push without an in-process handler succeeded")
	}
	var mu sync.Mutex
	var got []string
	status := 200
	m.SetInProcessPush("h1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+r.RemoteAddr+" "+string(b))
		mu.Unlock()
		if body["objectID"] == "slow" {
			<-r.Context().Done()
		}
		w.WriteHeader(status)
	}))
	if err := m.push("h1", "/v1/push/kill", map[string]any{"objectID": "o1", "reason": "test"}); err != nil {
		t.Fatalf("in-process push: %v", err)
	}
	if err := m.push("h1", "/v1/push/assignments", map[string]any{}); err != nil {
		t.Fatalf("in-process push: %v", err)
	}
	mu.Lock()
	if len(got) != 2 || got[0] != `POST /v1/push/kill inproc {"objectID":"o1","reason":"test"}` || got[1] != "POST /v1/push/assignments inproc {}" {
		t.Fatalf("pushes seen: %q", got)
	}
	mu.Unlock()
	// A non-2xx answer is an error; a hung handler is bounded by PushTimeout.
	status = 500
	if err := m.push("h1", "/v1/push/kill", map[string]any{"objectID": "o2"}); err == nil {
		t.Fatal("a 500 push counted as delivered")
	}
	status = 200
	st := time.Now()
	if err := m.push("h1", "/v1/push/kill", map[string]any{"objectID": "slow"}); err != nil || time.Since(st) < 900*time.Millisecond || time.Since(st) > 5*time.Second {
		t.Fatalf("slow push: %v after %v (PushTimeout 1 s; a handler that returns after the deadline is a 200)", err, time.Since(st))
	}
	// Forgetting the handler stops pushes; an embedded host is not dialled.
	m.SetInProcessPush("h1", nil)
	if err := m.push("h1", "/v1/push/kill", map[string]any{"objectID": "o3"}); err == nil {
		t.Fatal("push after the handler was forgotten succeeded")
	}
}
