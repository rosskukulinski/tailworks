package mgmt

// Tenant notifications (ADR-047): when a Workflow
// starts waiting for an approval, mgmt POSTs a JSON event with a deep link
// to the admin UI's approvals page to the tenant's webhook. Nobody else is
// told today (docs/approvals.md), so an approval waits until someone
// happens to look.
//
// The trigger is the host's approval_requested audit record, when mgmt
// stores it (IngestAudit): a record is stored once per chain position, so
// each wait notifies once, and only for a workload the submitting host
// serves. The webhook's config, URL included (a chat webhook's path is its
// credential), lives only in custody, like the OTLP export (telemetry.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Notification events.
const (
	EventApprovalRequested = "approval_requested"
	EventTest              = "test"
)

// notifyEvents are the events a webhook may subscribe to.
var notifyEvents = []string{
	EventApprovalRequested,
}

// Notification limits.
const (
	NotifyMaxURL     = 2048
	notifyTimeout    = 10 * time.Second
	notifyMaxRetries = 3
)

// notifyBackoff is the wait before each retry (tests shorten it).
var notifyBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// notifyAllowLoopback lets tests deliver to an httptest server on
// 127.0.0.1. In production loopback and link-local targets are refused.
var notifyAllowLoopback = false

// notifyReservedHeaders are set by mgmt itself.
var notifyReservedHeaders = map[string]bool{
	"tw-event":   true,
	"user-agent": true,
}

// notifyState is Mgmt's notification state (one field in Mgmt).
type notifyState struct {
	mu         sync.Mutex
	consoleURL string // https://tw-mgmt.<tailnet>, "" when unknown
}

// SetConsoleURL sets the admin UI's base URL used in notification links
// (internal/mgmtd: mgmt's own MagicDNS name).
func (m *Mgmt) SetConsoleURL(u string) {
	m.notifs.mu.Lock()
	m.notifs.consoleURL = strings.TrimSuffix(u, "/")
	m.notifs.mu.Unlock()
}

func (m *Mgmt) consoleURL() string {
	m.notifs.mu.Lock()
	defer m.notifs.mu.Unlock()
	return m.notifs.consoleURL
}

// ConsoleURL is the admin UI's base URL on mgmt's node ("" without one).
func (s SelfInfo) ConsoleURL() string {
	if s.DNSName == "" {
		return ""
	}
	scheme := "http://"
	if s.HTTPS {
		scheme = "https://"
	}
	return scheme + strings.TrimSuffix(s.DNSName, ".")
}

// Webhook is the tenant's notification webhook as custody holds it.
type Webhook struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Events  []string          `json:"events"`
}

// String never prints the URL's path or the header values.
func (h Webhook) String() string {
	return fmt.Sprintf("Webhook{%s, headers %v [REDACTED], %v}", urlOrigin(h.URL), sortedKeys(h.Headers), h.Events)
}

type notifyEntry struct {
	Webhook   *Webhook `json:"webhook,omitempty"`
	UpdatedAt int64    `json:"updatedAt"`
	UpdatedBy string   `json:"updatedBy,omitempty"`
}

// NotificationView is GET's answer.
type NotificationView struct {
	TenantID  string       `json:"tenantID"`
	Webhook   *WebhookView `json:"webhook,omitempty"`
	UpdatedAt int64        `json:"updatedAt,omitempty"`
	UpdatedBy string       `json:"updatedBy,omitempty"`
}

// WebhookView is a Webhook without its secrets.
type WebhookView struct {
	Origin      string   `json:"origin"`
	HeaderNames []string `json:"headerNames"`
	Events      []string `json:"events"`
}

func (e *notifyEntry) view(tenantID string) NotificationView {
	v := NotificationView{TenantID: tenantID, UpdatedAt: e.UpdatedAt, UpdatedBy: e.UpdatedBy}
	if e.Webhook != nil {
		v.Webhook = &WebhookView{Origin: urlOrigin(e.Webhook.URL), HeaderNames: sortedKeys(e.Webhook.Headers), Events: e.Webhook.Events}
	}
	return v
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// urlOrigin is scheme://host of s, never its path or query.
func urlOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return u.Scheme + "://" + u.Host
}

var notifyMu sync.Mutex

func notifyCustodyName(tenantID string) string { return "tw/" + tenantID + "/notifications" }

// ValidateWebhook checks and normalises h (events default to
// approval_requested, header names lowercase). Errors never quote the URL's
// path or a header value.
func ValidateWebhook(h *Webhook) error {
	if h == nil {
		return errf(400, "bad_request", "webhook is required")
	}
	if len(h.URL) > NotifyMaxURL {
		return errf(400, "bad_request", "webhook.url is longer than %d bytes", NotifyMaxURL)
	}
	u, err := url.Parse(h.URL)
	switch {
	case err != nil:
		return errf(400, "bad_request", "webhook.url is not a URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return errf(400, "bad_request", "webhook.url must be an http:// or https:// URL")
	case u.Hostname() == "":
		return errf(400, "bad_request", "webhook.url needs a host")
	case u.User != nil:
		return errf(400, "bad_request", "webhook.url must not carry user info; use a header")
	case u.Fragment != "":
		return errf(400, "bad_request", "webhook.url must not have a fragment")
	}
	if len(h.Headers) > TelemetryMaxHeaders {
		return errf(400, "bad_request", "at most %d headers", TelemetryMaxHeaders)
	}
	hs := make(map[string]string, len(h.Headers))
	for k, v := range h.Headers {
		lk := strings.ToLower(k)
		if !headerNameRE.MatchString(k) || telemetryReservedHeaders[lk] || notifyReservedHeaders[lk] {
			return errf(400, "bad_request", "header %q is not a valid (or allowed) header name", k)
		}
		if len(v) > TelemetryMaxHeaderValue || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n\x00") {
			return errf(400, "bad_request", "the value of header %q must be one line of UTF-8 text, at most %d bytes", k, TelemetryMaxHeaderValue)
		}
		if _, dup := hs[lk]; dup {
			return errf(400, "bad_request", "header %q is given twice", k)
		}
		hs[lk] = v
	}
	h.Headers = hs
	if len(h.Events) == 0 {
		h.Events = []string{EventApprovalRequested}
	}
	seen := map[string]bool{}
	for _, e := range h.Events {
		if !slices.Contains(notifyEvents, e) {
			return errf(400, "bad_request", "unknown event %q (approval_requested)", e)
		}
		seen[e] = true
	}
	h.Events = h.Events[:0]
	for _, e := range notifyEvents {
		if seen[e] {
			h.Events = append(h.Events, e)
		}
	}
	return nil
}

func (m *Mgmt) loadNotify(ctx context.Context, tenantID string) (*notifyEntry, error) {
	b, err := m.custody.Get(ctx, notifyCustodyName(tenantID))
	if errors.Is(err, ErrNotFound) {
		return &notifyEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("custody: %w", err)
	}
	var e notifyEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, errors.New("custody: notification config does not decode") // never echo the content
	}
	return &e, nil
}

// Notifications returns the tenant's config without secrets.
func (m *Mgmt) Notifications(ctx context.Context, tenantID string) (NotificationView, error) {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return NotificationView{}, err
	}
	e, err := m.loadNotify(ctx, tenantID)
	if err != nil {
		return NotificationView{}, err
	}
	return e.view(tenantID), nil
}

// PutNotifications sets the tenant's webhook. Audited with the URL's
// origin and the header names only.
func (m *Mgmt) PutNotifications(ctx context.Context, tenantID string, h Webhook, by string) (NotificationView, error) {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return NotificationView{}, err
	}
	if err := ValidateWebhook(&h); err != nil {
		return NotificationView{}, err
	}
	notifyMu.Lock()
	defer notifyMu.Unlock()
	e := &notifyEntry{Webhook: &h, UpdatedAt: m.now().UnixMilli(), UpdatedBy: by}
	b, err := json.Marshal(e)
	if err != nil {
		return NotificationView{}, err
	}
	if err := m.custody.Put(ctx, notifyCustodyName(tenantID), b); err != nil {
		return NotificationView{}, fmt.Errorf("custody: %w", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "notification_put", ActingFor: by},
		map[string]any{"origin": urlOrigin(h.URL), "headers": sortedKeys(h.Headers), "events": h.Events, "by": by})
	return e.view(tenantID), nil
}

// DeleteNotifications stops the tenant's notifications (idempotent).
func (m *Mgmt) DeleteNotifications(ctx context.Context, tenantID, by string) error {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return err
	}
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if err := m.custody.Delete(ctx, notifyCustodyName(tenantID)); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("custody: %w", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "notification_delete", ActingFor: by}, map[string]any{"by": by})
	return nil
}

// NotificationEvent is the body POSTed to a webhook.
type NotificationEvent struct {
	Event    string             `json:"event"`
	TenantID string             `json:"tenantID"`
	Text     string             `json:"text"`
	Link     string             `json:"link,omitempty"`
	CLI      string             `json:"cli,omitempty"`
	Workload string             `json:"workload,omitempty"`
	Workflow string             `json:"workflow,omitempty"`
	Instance string             `json:"instance,omitempty"`
	Step     string             `json:"step,omitempty"`
	Facts    map[string]float64 `json:"facts,omitempty"`
	SentAt   time.Time          `json:"sentAt"`
}

// ApprovalLink is the admin UI's approvals page for tenantID, scrolled to
// instance's row (its id attribute). base "" gives a path only.
func ApprovalLink(base, tenantID, instance string) string {
	l := base + "/ui/approvals?tenant=" + url.QueryEscape(tenantID)
	if instance != "" {
		l += "#" + (&url.URL{Fragment: instance}).EscapedFragment()
	}
	return l
}

// notifyHostRecord sends approval_requested notifications for a host
// record mgmt just stored. o is the record's object (nil if unknown: then
// nothing is sent, since the tenant and workload can't be trusted).
func (m *Mgmt) notifyHostRecord(o *Object, kind string, raw json.RawMessage) {
	if kind != EventApprovalRequested || o == nil {
		return
	}
	var rec struct {
		Detail struct {
			Workload string             `json:"workload"`
			Workflow string             `json:"workflow"`
			Instance string             `json:"instance"`
			Step     string             `json:"step"`
			Facts    map[string]float64 `json:"facts"`
		} `json:"detail"`
	}
	if json.Unmarshal(raw, &rec) != nil || rec.Detail.Instance == "" {
		return
	}
	d := rec.Detail
	if d.Workload != "" && d.Workload != o.Workload {
		return // a record about another workload than its object's
	}
	ev := NotificationEvent{Event: EventApprovalRequested, TenantID: o.TenantID, Workload: o.Workload, Workflow: d.Workflow,
		Instance: d.Instance, Step: d.Step, Facts: d.Facts}
	ev.CLI = "tw approve " + o.Workload + " " + d.Instance
	if d.Workflow != "" {
		ev.CLI += " --workflow " + d.Workflow
	}
	base := m.consoleURL()
	if base != "" {
		ev.Link = ApprovalLink(base, o.TenantID, d.Instance)
	}
	ev.Text = approvalText(ev)
	m.sendAsync(o.TenantID, ev)
}

func approvalText(ev NotificationEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Approval needed: %s/%s instance %s", ev.Workload, ev.Workflow, ev.Instance)
	if len(ev.Facts) > 0 {
		keys := make([]string, 0, len(ev.Facts))
		for k := range ev.Facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, ev.Facts[k]))
		}
		b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	if ev.Link != "" {
		b.WriteString(". Decide: " + ev.Link)
	} else {
		b.WriteString(". Decide: " + ev.CLI)
	}
	return b.String()
}

// sendAsync delivers ev to the tenant's webhook in the background (if one
// subscribes to it), retrying, and audits a final failure.
func (m *Mgmt) sendAsync(tenantID string, ev NotificationEvent) {
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		e, err := m.loadNotify(ctx, tenantID)
		if err != nil || e.Webhook == nil || !slices.Contains(e.Webhook.Events, ev.Event) {
			return
		}
		var last string
		for attempt := 0; attempt < notifyMaxRetries; attempt++ {
			if attempt > 0 {
				wait := notifyBackoff[min(attempt-1, len(notifyBackoff)-1)]
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			ev.SentAt = m.now().UTC()
			res := m.deliver(ctx, *e.Webhook, ev)
			if res.Delivered {
				return
			}
			last = res.Error
		}
		m.log.Warn("notification not delivered", "tenant", tenantID, "event", ev.Event, "webhook", urlOrigin(e.Webhook.URL), "err", last)
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "notification_failed"},
			map[string]any{"event": ev.Event, "origin": urlOrigin(e.Webhook.URL), "workload": ev.Workload, "instance": ev.Instance, "error": last})
	}()
}

// NotifyResult is one delivery attempt.
type NotifyResult struct {
	Delivered bool   `json:"delivered"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TestNotifications sends one test event now.
func (m *Mgmt) TestNotifications(ctx context.Context, tenantID, by string) (NotifyResult, error) {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return NotifyResult{}, err
	}
	e, err := m.loadNotify(ctx, tenantID)
	if err != nil {
		return NotifyResult{}, err
	}
	if e.Webhook == nil {
		return NotifyResult{}, errf(404, "not_found", "tenant %s has no notification webhook; tw notify set --webhook <url>", tenantID)
	}
	ev := NotificationEvent{Event: EventTest, TenantID: tenantID, SentAt: m.now().UTC(), Link: ApprovalLink(m.consoleURL(), tenantID, "")}
	ev.Text = "tw test notification for tenant " + tenantID + ". Approvals: " + ev.Link
	res := m.deliver(ctx, *e.Webhook, ev)
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "notification_test", ActingFor: by},
		map[string]any{"origin": urlOrigin(e.Webhook.URL), "delivered": res.Delivered, "status": res.Status, "error": res.Error, "by": by})
	return res, nil
}

// deliver POSTs ev once. Errors never carry the URL (its path is secret).
func (m *Mgmt) deliver(ctx context.Context, h Webhook, ev NotificationEvent) NotifyResult {
	body, _ := json.Marshal(ev)
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return NotifyResult{Error: "bad webhook URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tw-mgmt")
	req.Header.Set("Tw-Event", ev.Event)
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	res, err := m.notifyClient(req.URL).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		return NotifyResult{Error: err.Error()}
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return NotifyResult{Status: res.StatusCode, Error: "webhook answered " + res.Status}
	}
	return NotifyResult{Delivered: true, Status: res.StatusCode}
}

// tailnetRanges are Tailscale's address ranges.
var tailnetRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// onTailnet reports whether host is reached over mgmt's tailnet node: a
// one-label name (MagicDNS short name), a *.ts.net name, or a Tailscale
// address.
func onTailnet(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		for _, p := range tailnetRanges {
			if p.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return !strings.Contains(h, ".") || strings.HasSuffix(h, ".ts.net")
}

// notifyClient dials target's host over the tailnet (Config.TailnetDial)
// or the host's network, never following redirects, and refusing loopback,
// link-local and unspecified addresses on the host's network.
func (m *Mgmt) notifyClient(target *url.URL) *http.Client {
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	if onTailnet(target.Hostname()) && m.cfg.TailnetDial != nil {
		dial = m.cfg.TailnetDial
	} else {
		d := &net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			ip := ap.Addr().Unmap()
			if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || (ip.IsLoopback() && !notifyAllowLoopback) {
				return fmt.Errorf("webhook address %s is not allowed (loopback or link-local)", ip)
			}
			return nil
		}}
		dial = d.DialContext
	}
	return &http.Client{
		Transport:     &http.Transport{DialContext: dial, Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: notifyTimeout},
		Timeout:       notifyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ---- /v1 handlers ----

func (a *v1api) GetTenantNotifications(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	v, err := a.s.M.Notifications(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *v1api) PutTenantNotifications(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Webhook *Webhook `json:"webhook"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Webhook == nil {
		// Never echo the decoder's error: it can quote the URL or a header value.
		writeErr(w, r, errf(400, "bad_request", `the body must be {"webhook": {"url": "<url>", "headers": {…}, "events": ["approval_requested"]}}`))
		return
	}
	v, err := a.s.M.PutNotifications(r.Context(), id, *body.Webhook, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *v1api) DeleteTenantNotifications(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	if err := a.s.M.DeleteNotifications(r.Context(), id, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) TestTenantNotifications(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	res, err := a.s.M.TestNotifications(r.Context(), id, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}
