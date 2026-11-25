package fakemgmt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/queuecfg"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// Queues (ADR-039), in memory: the same claim / settle / dead-letter
// semantics as tw-mgmt (mgmt/queues.go), fenced by the acting workload's
// lease. Not sealed (nothing here is persisted), and the deployment's
// declarations aren't checked (hostd checks them before it calls).

type fakeQueue struct {
	name, inc             string
	seq                   int64
	msgs                  map[string]*fakeQMsg
	consumer              string
	consumedAt            int64
	created, updated      time.Time
	sends, pulls, settles int

	// claims counts messages handed to a host's pull (QueueClaims).
	claims int
}

type fakeQMsg struct {
	id, ct, lease string
	seq, attempts int64
	sent, visible int64
	body          []byte
}

func (s *Server) queueKey(r *http.Request) string {
	return r.PathValue("id") + "|" + r.PathValue("queue")
}

// queueLocked returns the queue at r's path, creating it if create.
func (s *Server) queueLocked(tenant, name string, create bool) *fakeQueue {
	if s.queues == nil {
		s.queues = map[string]*fakeQueue{}
	}
	k := tenant + "|" + name
	if q := s.queues[k]; q != nil || !create {
		return q
	}
	now := time.Now()
	q := &fakeQueue{name: name, inc: "qi_" + newID(""), msgs: map[string]*fakeQMsg{}, created: now, updated: now}
	s.queues[k] = q
	return q
}

// fenceQueue: a caller naming workload and epoch must hold that lease.
func (s *Server) fenceQueue(w http.ResponseWriter, r *http.Request) (workload string, ok bool) {
	if !s.knownTenant(w, r) {
		return "", false
	}
	if !queuecfg.ValidName(r.PathValue("queue")) {
		writeErr(w, 400, "bad_request", "bad queue name")
		return "", false
	}
	workload = r.URL.Query().Get("workload")
	if workload == "" {
		return "", true // an admin
	}
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	name := mgmtapi.LeaseName(r.PathValue("id"), workload)
	l := s.leases[name]
	if l == nil || l.epoch != epoch {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, fmt.Sprintf("lease %s is not held at epoch %d", name, epoch))
		return "", false
	}
	if hid := r.Header.Get("Tw-Host-ID"); hid != "" && hid != l.host {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, "host "+hid+" does not hold the lease")
		return "", false
	}
	return workload, true
}

func (s *Server) queueView(tenant string, q *fakeQueue) map[string]any {
	now := time.Now().UnixMilli()
	var bytes, inflight, delayed, oldest int64
	for _, m := range q.msgs {
		bytes += int64(len(m.body))
		if m.visible > now {
			if m.lease != "" {
				inflight++
			} else {
				delayed++
			}
		}
		if oldest == 0 || m.sent < oldest {
			oldest = m.sent
		}
	}
	v := map[string]any{"tenantID": tenant, "name": q.name, "incarnation": q.inc, "backlog": len(q.msgs), "bytes": bytes,
		"inFlight": inflight, "delayed": delayed, "createdAt": ms(q.created), "updatedAt": ms(q.updated)}
	if oldest > 0 {
		v["oldestAt"] = oldest
	}
	if q.consumer != "" {
		v["consumer"], v["consumedAt"] = q.consumer, q.consumedAt
	}
	return v
}

// QueueStats reports how many sends, claims and settles a queue saw
// (tests).
func (s *Server) QueueStats(tenantID, queue string) (sends, pulls, settles int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queueLocked(tenantID, queue, false); q != nil {
		return q.sends, q.pulls, q.settles
	}
	return 0, 0, 0
}

// QueueBacklog is a queue's message count (tests; -1: no queue).
func (s *Server) QueueBacklog(tenantID, queue string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queueLocked(tenantID, queue, false); q != nil {
		return len(q.msgs)
	}
	return -1
}

func (s *Server) listQueues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	prefix := r.PathValue("id") + "|"
	items := []map[string]any{}
	for k, q := range s.queues {
		if strings.HasPrefix(k, prefix) {
			items = append(items, s.queueView(r.PathValue("id"), q))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) getQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), false)
	if q == nil {
		writeErr(w, 404, "unknown_queue", "no such queue")
		return
	}
	writeJSON(w, 200, s.queueView(r.PathValue("id"), q))
}

func (s *Server) putQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	existed := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), false) != nil
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), true)
	status := 201
	if existed {
		status = 200
	}
	writeJSON(w, status, s.queueView(r.PathValue("id"), q))
}

func (s *Server) deleteQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	k := s.queueKey(r)
	if s.queues[k] == nil {
		writeErr(w, 404, "unknown_queue", "no such queue")
		return
	}
	delete(s.queues, k)
	w.WriteHeader(204)
}

func (s *Server) purgeQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), false)
	if q == nil {
		writeErr(w, 404, "unknown_queue", "no such queue")
		return
	}
	n := len(q.msgs)
	q.msgs = map[string]*fakeQMsg{}
	writeJSON(w, 200, map[string]any{"purged": n})
}

func (q *fakeQueue) sorted() []*fakeQMsg {
	out := make([]*fakeQMsg, 0, len(q.msgs))
	for _, m := range q.msgs {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].visible != out[j].visible {
			return out[i].visible < out[j].visible
		}
		return out[i].seq < out[j].seq
	})
	return out
}

func qmsgView(m *fakeQMsg, lease bool) map[string]any {
	v := map[string]any{"id": m.id, "body": m.body, "contentType": m.ct, "attempts": m.attempts, "timestamp": m.sent}
	if lease {
		v["leaseId"] = m.id + ":" + m.lease
	} else {
		v["visibleAt"] = m.visible
	}
	return v
}

func (s *Server) peekQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), false)
	if q == nil {
		writeErr(w, 404, "unknown_queue", "no such queue")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	all := q.sorted()
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	out := []map[string]any{}
	for _, m := range all {
		if len(out) == limit {
			break
		}
		out = append(out, qmsgView(m, false))
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}

func (s *Server) sendQueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages     []mgmtapi.QueueMessageIn `json:"messages"`
		DelaySeconds int                      `json:"delaySeconds"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.fenceQueue(w, r); !ok {
		return
	}
	if len(body.Messages) == 0 || len(body.Messages) > queuecfg.MaxBatchMessages {
		writeErr(w, 400, "queue_limit", "1 to 100 messages")
		return
	}
	total := 0
	for _, m := range body.Messages {
		if len(m.Body) > queuecfg.MaxMessageBytes {
			writeErr(w, 400, "queue_limit", "message too large")
			return
		}
		total += len(m.Body)
	}
	if total > queuecfg.MaxBatchBytes {
		writeErr(w, 400, "queue_limit", "batch too large")
		return
	}
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), true)
	now := time.Now().UnixMilli()
	ids := []string{}
	for _, m := range body.Messages {
		d := body.DelaySeconds
		if m.DelaySeconds != nil {
			d = *m.DelaySeconds
		}
		ct := m.ContentType
		if ct == "" {
			ct = "json"
		}
		q.seq++
		msg := &fakeQMsg{id: "qm_" + newID(""), ct: ct, seq: q.seq, sent: now, visible: now + int64(d)*1000, body: m.Body}
		q.msgs[msg.id] = msg
		ids = append(ids, msg.id)
	}
	q.sends++
	q.updated = time.Now()
	v := s.queueView(r.PathValue("id"), q)
	writeJSON(w, 201, map[string]any{"ids": ids, "backlog": v["backlog"], "bytes": v["bytes"], "oldestAt": v["oldestAt"]})
}

func leaseToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// deadLetterLocked moves m out of q (into dlq, if named).
func (s *Server) deadLetterLocked(tenant string, q *fakeQueue, m *fakeQMsg, dlq string) {
	delete(q.msgs, m.id)
	if dlq == "" {
		return
	}
	d := s.queueLocked(tenant, dlq, true)
	d.seq++
	d.msgs[m.id] = &fakeQMsg{id: m.id, ct: m.ct, seq: d.seq, sent: m.sent, visible: time.Now().UnixMilli(), body: m.body}
}

type fakeConsumer struct {
	maxRetries int64
	dlq        string
	retryDelay int
}

func (s *Server) pullQueue(w http.ResponseWriter, r *http.Request) {
	var o struct {
		mgmtapi.QueuePullOpts
		MaxRetries      *int   `json:"maxRetries"`
		DeadLetterQueue string `json:"deadLetterQueue"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&o); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if o.BatchSize <= 0 {
		o.BatchSize = queuecfg.DefaultBatchSize
	}
	if o.VisibilityTimeoutMs <= 0 {
		o.VisibilityTimeoutMs = 30_000
	}
	deadline := time.Now().Add(time.Duration(o.WaitMs) * time.Millisecond)
	for {
		s.mu.Lock()
		workload, ok := s.fenceQueue(w, r)
		if !ok {
			s.mu.Unlock()
			return
		}
		c := s.consumerLocked(r.PathValue("id"), workload, r.PathValue("queue"), o.MaxRetries, o.DeadLetterQueue, nil)
		q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), true)
		now := time.Now().UnixMilli()
		out := []map[string]any{}
		var claimed []fakeClaim
		dead := 0
		for _, m := range q.sorted() {
			if len(out) == o.BatchSize || m.visible > now {
				break
			}
			if m.attempts > c.maxRetries {
				s.deadLetterLocked(r.PathValue("id"), q, m, c.dlq)
				dead++
				continue
			}
			m.attempts++
			m.visible = now + o.VisibilityTimeoutMs
			m.lease = leaseToken()
			out = append(out, qmsgView(m, true))
			claimed = append(claimed, fakeClaim{m, m.lease})
		}
		if len(claimed) > 0 && r.Context().Err() != nil {
			// The caller left as the claim was made: hand it back
			// uncounted, as mgmt does (#412).
			releaseUndeliveredLocked(claimed, now)
			s.mu.Unlock()
			writeJSON(w, 200, map[string]any{"messages": []any{}})
			return
		}
		if len(out) > 0 || dead > 0 || !time.Now().Before(deadline) {
			q.pulls++
			if len(out) > 0 {
				if workload != "" {
					q.claims += len(out)
				}
				q.consumer, q.consumedAt = workload, now
				if workload == "" {
					q.consumer = "admin"
				}
			}
			s.mu.Unlock()
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(200)
			if err := json.NewEncoder(w).Encode(map[string]any{"messages": out, "deadLettered": dead}); err != nil && len(claimed) > 0 {
				s.mu.Lock() // the answer never reached the caller (#412)
				releaseUndeliveredLocked(claimed, time.Now().UnixMilli())
				s.mu.Unlock()
			}
			return
		}
		s.mu.Unlock()
		select {
		case <-r.Context().Done():
			writeJSON(w, 200, map[string]any{"messages": []any{}})
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// consumerLocked is the settings a claim or settle applies: the
// workload's deployment's consumer config for a host, the request's for an
// admin.
func (s *Server) consumerLocked(tenant, workload, queue string, maxRetries *int, dlq string, retryDelay *int) fakeConsumer {
	c := fakeConsumer{maxRetries: queuecfg.DefaultMaxRetries, dlq: dlq}
	if workload != "" {
		c.dlq = ""
		if k := s.deployedConsumerLocked(tenant, workload, queue); k != nil {
			return fakeConsumer{maxRetries: int64(k.Retries()), dlq: k.DeadLetterQueue, retryDelay: k.RetryDelay}
		}
		return c
	}
	if maxRetries != nil {
		c.maxRetries = int64(*maxRetries)
	}
	if retryDelay != nil {
		c.retryDelay = *retryDelay
	}
	return c
}

func (s *Server) ackQueue(w http.ResponseWriter, r *http.Request) {
	var a struct {
		mgmtapi.QueueAck
		RetryDelaySeconds *int   `json:"retryDelaySeconds"`
		MaxRetries        *int   `json:"maxRetries"`
		DeadLetterQueue   string `json:"deadLetterQueue"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if a.VisibilityTimeoutMs <= 0 {
		a.VisibilityTimeoutMs = 30_000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workload, ok := s.fenceQueue(w, r)
	if !ok {
		return
	}
	c := s.consumerLocked(r.PathValue("id"), workload, r.PathValue("queue"), a.MaxRetries, a.DeadLetterQueue, a.RetryDelaySeconds)
	res := mgmtapi.QueueAckResult{}
	q := s.queueLocked(r.PathValue("id"), r.PathValue("queue"), false)
	current := func(l string) *fakeQMsg {
		id, tok, _ := strings.Cut(l, ":")
		if q == nil {
			return nil
		}
		if m := q.msgs[id]; m != nil && m.lease != "" && m.lease == tok {
			return m
		}
		return nil
	}
	now := time.Now().UnixMilli()
	for _, l := range a.Acks {
		if m := current(l); m != nil {
			delete(q.msgs, m.id)
			res.Acked++
		} else {
			res.Stale++
		}
	}
	for _, rt := range a.Retries {
		m := current(rt.LeaseID)
		if m == nil {
			res.Stale++
			continue
		}
		if m.attempts > c.maxRetries {
			s.deadLetterLocked(r.PathValue("id"), q, m, c.dlq)
			res.DeadLettered++
			continue
		}
		d := c.retryDelay
		if rt.DelaySeconds != nil {
			d = *rt.DelaySeconds
		}
		m.visible, m.lease = now+int64(d)*1000, ""
		res.Retried++
	}
	for _, l := range a.Extend {
		if m := current(l); m != nil {
			m.visible = now + a.VisibilityTimeoutMs
			res.Extended++
		} else {
			res.Stale++
		}
	}
	for _, l := range a.Release {
		if m := current(l); m != nil {
			m.visible, m.lease, m.attempts = now, "", max(m.attempts-1, 0)
			res.Released++
		} else {
			res.Stale++
		}
	}
	if q != nil {
		q.settles++
	}
	writeJSON(w, 200, res)
}

// deployedConsumerLocked is the consumer of queue declared by the
// workload's latest deployment (nil: none).
func (s *Server) deployedConsumerLocked(tenant, workload, queue string) *queuecfg.Consumer {
	var latest *deployment
	for _, d := range s.deployments {
		if d.tenantID == tenant && d.workload == workload && (latest == nil || d.created.After(latest.created)) {
			latest = d
		}
	}
	if latest == nil {
		return nil
	}
	c, err := mgmt.BundleQueues(latest.bundle)
	if err != nil {
		return nil
	}
	return c.ConsumerOf(queue)
}

// fakeClaim is one message a pull leased, with the lease it got.
type fakeClaim struct {
	m     *fakeQMsg
	lease string
}

// releaseUndeliveredLocked hands back messages a pull claimed for a caller
// that never got them: visible at once, the claim's attempt taken back, as
// a Release does (mgmt/queueclaimgone.go). A message whose lease has moved
// on since is left alone.
func releaseUndeliveredLocked(cs []fakeClaim, now int64) {
	for _, c := range cs {
		if c.m.lease != c.lease {
			continue
		}
		c.m.visible, c.m.lease, c.m.attempts = now, "", max(c.m.attempts-1, 0)
	}
}
