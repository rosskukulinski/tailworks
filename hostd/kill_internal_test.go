package hostd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
)

// killHost is a host with one ready object (tn/probe Probe/c) whose
// workload's workerd admin endpoint is admin.
func killHost(t *testing.T, admin http.HandlerFunc) (*Host, *object) {
	t.Helper()
	h, _ := ctrHost(t, nil)
	inst := ctrInst(h, "tn", "probe")
	srv := httptest.NewServer(admin)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	inst.direct = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}}
	inst.w.active = inst
	o := &object{ID: "obj_c", NetdID: "n_c", TenantID: "tn", Workload: "probe", Class: "Probe", Name: "c",
		Hostname: "c", Status: statusReady, readyCh: make(chan struct{})}
	h.mu.Lock()
	h.objects[o.ID] = o
	h.byKey[o.key()] = o
	h.mu.Unlock()
	return h, o
}

func isAdminKill(r *http.Request) bool {
	return r.URL.Path == "/_tw/admin" && strings.Contains(r.Header.Get(netd.HeaderCF), `"op":"kill"`)
}

// TestKillWaitsForRunningKill (#94): a kill that finds the object already
// being killed (mgmt's push racing the heartbeat's RevokeObjects) must not
// return before the in-process kill has landed in workerd. Before the fix
// it returned at once, so `tw kill` could return while the workload's
// in-process path still reached the live object.
func TestKillWaitsForRunningKill(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var kills atomic.Int32
	h, o := killHost(t, func(w http.ResponseWriter, r *http.Request) {
		if !isAdminKill(r) {
			http.Error(w, "unexpected", 400)
			return
		}
		if kills.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Write([]byte(`{"ok":true}`))
	})
	released := false
	t.Cleanup(func() { // before the server's Close, which waits for the held request
		if !released {
			close(release)
		}
	})
	first := make(chan error, 1)
	go func() { first <- h.killLocal(o.ID, "revoked by mgmt") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first kill never reached workerd")
	}
	// The push arrives while the first kill waits on workerd.
	second := make(chan error, 1)
	go func() { second <- h.killLocal(o.ID, "test kill") }()
	select {
	case err := <-second:
		t.Fatalf("the second kill returned (%v) before workerd confirmed the in-process kill", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	released = true
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("kill did not return after workerd answered")
		}
	}
	if n := kills.Load(); n != 1 {
		t.Fatalf("workerd got %d kills, want 1", n)
	}
	// An already finished kill returns at once.
	if err := h.killLocal(o.ID, "again"); err != nil || kills.Load() != 1 {
		t.Fatalf("repeat kill: %v (workerd kills %d)", err, kills.Load())
	}
}

// TestKillInProcessFailureIsReported (#94): when workerd does not confirm
// the in-process kill, the push answers 5xx (mgmt does not count it as
// pushed) and the next kill of the object (the heartbeat's) tries again.
func TestKillInProcessFailureIsReported(t *testing.T) {
	old := killInProcessBackoff
	killInProcessBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { killInProcessBackoff = old })
	var ok atomic.Bool
	var kills atomic.Int32
	h, o := killHost(t, func(w http.ResponseWriter, r *http.Request) {
		if !isAdminKill(r) {
			http.Error(w, "unexpected", 400)
			return
		}
		kills.Add(1)
		if !ok.Load() {
			http.Error(w, `{"code":"bad_request","message":"kill: busy"}`, 500)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	mux := http.NewServeMux()
	h.pushHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/push/kill", strings.NewReader(`{"objectID":"obj_c","reason":"test kill"}`)))
	if rec.Code/100 == 2 {
		t.Fatalf("push answered %d with the in-process kill failing: %s", rec.Code, rec.Body)
	}
	if n := kills.Load(); n != 2 {
		t.Fatalf("workerd got %d kill attempts, want 2 (one retry)", n)
	}
	h.mu.Lock()
	st := o.Status
	h.mu.Unlock()
	if st != statusKilled {
		t.Fatalf("status %q: the node must be cut off even when workerd fails", st)
	}
	ok.Store(true)
	if err := h.killLocal(o.ID, "revoked by mgmt"); err != nil {
		t.Fatalf("retried kill: %v", err)
	}
	if n := kills.Load(); n != 3 {
		t.Fatalf("workerd got %d kill attempts, want 3", n)
	}
	if err := h.killLocal(o.ID, "again"); err != nil || kills.Load() != 3 {
		t.Fatalf("kill after a successful retry: %v (workerd kills %d)", err, kills.Load())
	}
}
