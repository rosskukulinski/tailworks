package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const hookSecret = "T000/B000/hook-s3cret"

// webhookSink records what mgmt POSTs; it answers status (200 by default).
type webhookSink struct {
	mu     sync.Mutex
	got    []NotificationEvent
	hdrs   []http.Header
	status int
}

func newSink(t *testing.T) (*webhookSink, *httptest.Server) {
	s := &webhookSink{status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var ev NotificationEvent
		json.Unmarshal(b, &ev)
		s.mu.Lock()
		s.got = append(s.got, ev)
		s.hdrs = append(s.hdrs, r.Header.Clone())
		st := s.status
		s.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *webhookSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func allowLoopbackNotify(t *testing.T) {
	oldL, oldB := notifyAllowLoopback, notifyBackoff
	notifyAllowLoopback, notifyBackoff = true, []time.Duration{time.Millisecond}
	t.Cleanup(func() { notifyAllowLoopback, notifyBackoff = oldL, oldB })
}

// TestNotificationsConfig (ADR-047): PUT/GET/DELETE of
// a tenant's webhook. The URL's path and the header values are secrets:
// never returned, audited or logged.
func TestNotificationsConfig(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c := e.c
	tn := v1Tenant(t, e, "t.ts.net", nil)
	path := "/v1/tenants/" + tn + "/notifications"

	var v NotificationView
	c.ok("GET", path, nil, &v)
	if v.TenantID != tn || v.Webhook != nil {
		t.Fatalf("empty: %+v", v)
	}
	c.ok("PUT", path, map[string]any{"webhook": map[string]any{"url": "https://hooks.slack.com/services/" + hookSecret, "headers": map[string]string{"X-Token": "tok-s3cret"}}}, &v)
	if v.Webhook == nil || v.Webhook.Origin != "https://hooks.slack.com" || fmt.Sprint(v.Webhook.HeaderNames) != "[x-token]" ||
		fmt.Sprint(v.Webhook.Events) != "[approval_requested]" || v.UpdatedBy == "" {
		t.Fatalf("put: %+v", v.Webhook)
	}
	st, raw, _ := c.doRaw("GET", path, nil, nil)
	if st != 200 || strings.Contains(fmt.Sprint(raw), "s3cret") || strings.Contains(fmt.Sprint(raw), "/services/") {
		t.Fatalf("get leaks: %d %v", st, raw)
	}
	for _, body := range []any{
		map[string]any{},
		map[string]any{"webhook": map[string]any{"url": "hooks.slack.com/x"}},
		map[string]any{"webhook": map[string]any{"url": "ftp://x/y"}},
		map[string]any{"webhook": map[string]any{"url": "https://u:p@x/y"}},
		map[string]any{"webhook": map[string]any{"url": "https://x/y#frag"}},
		map[string]any{"webhook": map[string]any{"url": "https://x/y", "events": []string{"deploy"}}},
		map[string]any{"webhook": map[string]any{"url": "https://x/y", "headers": map[string]string{"tw-event": "x"}}},
		map[string]any{"webhook": map[string]any{"url": "https://x/y", "headers": map[string]string{"a": "line\nbreak tok-s3cret"}}},
		map[string]any{"webhook": map[string]any{"url": "https://x/" + hookSecret}, "extra": 1},
	} {
		st, eb := direct(t, e, "", "PUT", path, body)
		if st != 400 || eb["code"] != "bad_request" || strings.Contains(fmt.Sprint(eb), "s3cret") {
			t.Fatalf("PUT %v: %d %v", body, st, eb)
		}
	}
	c.fail("PUT", "/v1/tenants/tn_nope/notifications", map[string]any{"webhook": map[string]any{"url": "https://x/y"}}, 404, "unknown_tenant")
	c.ok("DELETE", path, nil, nil)
	c.ok("DELETE", path, nil, nil)
	c.fail("POST", path+"/test", nil, 404, "not_found")
	recs, err := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn, Kinds: []string{"notification_put", "notification_delete"}})
	if err != nil || len(recs) != 3 || !strings.Contains(string(recs[0].Detail), `"origin":"https://hooks.slack.com"`) {
		t.Fatalf("audit: %d %v", len(recs), err)
	}
	assertNoValue(t, e, "s3cret")
}

// TestApprovalRequestedNotification: a stored approval_requested host
// record POSTs one event with the deep link and the tailworks approve command;
// a failing webhook is retried and then audited, without the secret path.
func TestApprovalRequestedNotification(t *testing.T) {
	allowLoopbackNotify(t)
	e := newTestEnv(t)
	tn := v1Tenant(t, e, "t.ts.net", nil)
	e.m.SetConsoleURL(SelfInfo{DNSName: "tw-mgmt.t.ts.net.", HTTPS: true}.ConsoleURL())
	sink, srv := newSink(t)
	e.c.ok("PUT", "/v1/tenants/"+tn+"/notifications", map[string]any{"webhook": map[string]any{"url": srv.URL + "/hook/" + hookSecret, "headers": map[string]string{"authorization": "Bearer tok"}}}, nil)

	// The test endpoint delivers synchronously.
	var res NotifyResult
	e.c.ok("POST", "/v1/tenants/"+tn+"/notifications/test", nil, &res)
	if !res.Delivered || res.Status != 200 || sink.count() != 1 || sink.got[0].Event != "test" {
		t.Fatalf("test: %+v %+v", res, sink.got)
	}

	obj := &Object{ID: "obj_wl", TenantID: tn, Workload: "refunds"}
	rec := func(workload, instance string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"kind": "approval_requested", "detail": map[string]any{"workload": workload, "workflow": "refund", "instance": instance,
			"step": "wait-for-approval", "facts": map[string]float64{"amount": 120}}})
		return b
	}
	e.m.notifyHostRecord(obj, "approval_requested", rec("refunds", "inst 1"))
	e.m.notifyHostRecord(obj, "approval_decision", rec("refunds", "x"))  // not an event
	e.m.notifyHostRecord(nil, "approval_requested", rec("refunds", "y")) // unknown object
	e.m.notifyHostRecord(obj, "approval_requested", rec("payroll", "z")) // another workload's record
	waitFor(t, "2 events", func() bool { return sink.count() == 2 })
	time.Sleep(50 * time.Millisecond)
	if sink.count() != 2 {
		t.Fatalf("sent %d events, want 2", sink.count())
	}
	ev, h := sink.got[1], sink.hdrs[1]
	wantLink := "https://tw-mgmt.t.ts.net/ui/approvals?tenant=" + tn + "#inst%201"
	if ev.Event != "approval_requested" || ev.TenantID != tn || ev.Workload != "refunds" || ev.Instance != "inst 1" || ev.Link != wantLink ||
		ev.CLI != "tw approve refunds inst 1 --workflow refund" || ev.Facts["amount"] != 120 || !strings.Contains(ev.Text, wantLink) ||
		!strings.Contains(ev.Text, "amount=120") {
		t.Fatalf("event: %+v", ev)
	}
	if h.Get("Tw-Event") != "approval_requested" || h.Get("Authorization") != "Bearer tok" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", h)
	}

	// A failing webhook: tried notifyMaxRetries times, then audited.
	sink.mu.Lock()
	sink.status = 500
	sink.mu.Unlock()
	e.m.notifyHostRecord(obj, "approval_requested", rec("refunds", "inst-2"))
	waitFor(t, "notification_failed", func() bool {
		recs, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn, Kinds: []string{"notification_failed"}})
		return len(recs) == 1
	})
	if sink.count() != 2+notifyMaxRetries {
		t.Fatalf("attempts: %d", sink.count()-2)
	}
	recs, _ := e.store.ListAudit(context.Background(), AuditFilter{TenantID: tn, Kinds: []string{"notification_failed"}})
	if d := string(recs[0].Detail); !strings.Contains(d, "500") || !strings.Contains(d, "inst-2") {
		t.Fatalf("failure audit: %s", d)
	}
	assertNoValue(t, e, "s3cret")
}

// TestNotifyDialPolicy: loopback and link-local targets are refused on the
// host's network; tailnet names go over the tailnet dialer.
func TestNotifyDialPolicy(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	for _, u := range []string{"http://127.0.0.1:9/x", "http://169.254.169.254/latest", "http://[::1]:9/"} {
		res := e.m.deliver(context.Background(), Webhook{URL: u}, NotificationEvent{Event: "test"})
		if res.Delivered || !strings.Contains(res.Error, "not allowed") {
			t.Fatalf("%s: %+v", u, res)
		}
	}
	for host, want := range map[string]bool{"chat-bot": true, "bot.tail123.ts.net": true, "100.101.102.103": true, "fd7a:115c:a1e0::1": true,
		"hooks.slack.com": false, "8.8.8.8": false, "127.0.0.1": false} {
		if onTailnet(host) != want {
			t.Errorf("onTailnet(%s) = %v", host, !want)
		}
	}
	if l := ApprovalLink("", "tn_x", ""); l != "/ui/approvals?tenant=tn_x" {
		t.Errorf("link without base: %s", l)
	}
}
