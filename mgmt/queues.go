package mgmt

// Queues (ADR-039): a tenant's Cloudflare-style queues, brokered here.
// Producers' hosts send, push consumers' hosts claim batches with a
// visibility timeout and settle them, and admins (operators and HTTP pull
// consumers) do the same plus create, inspect, purge and delete.
//
//   - Messages are rows in mgmt's database, so a consumer moving host, a
//     hostd restart or a host loss loses nothing: a claimed message whose
//     lease expires is claimed again (at-least-once).
//   - Bodies are sealed with a per-queue data key in custody (AES-256-GCM,
//     bound to the queue's incarnation and the message ID); deleting a
//     queue crypto-shreds it.
//   - A host acts for a workload: it must hold the workload's lease at the
//     epoch it names (lease_lost), and the workload's latest deployment must
//     declare the queue (queues.producers to send, queues.consumers to
//     claim and settle). The consumer's settings (max_retries,
//     dead_letter_queue, retry_delay) come from that deployment. The
//     deploy is the grant.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/internal/queuecfg"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
	"github.com/tailscale/hujson"
)

// Queue limits beyond queuecfg's (Cloudflare's per-message and per-batch
// ones live there).
const (
	QueueMaxBytes          = 1 << 30
	queueDefaultVisibility = 30_000
	queueMaxWait           = 20 * time.Second
	queuePeekDefault       = 20
)

// QueueInfo is a queue and its counters (GET /v1/tenants/{id}/queues/{q}).
type QueueInfo struct {
	TenantID    string `json:"tenantID"`
	Name        string `json:"name"`
	Incarnation string `json:"incarnation"`
	Backlog     int64  `json:"backlog"`
	Bytes       int64  `json:"bytes"`
	InFlight    int64  `json:"inFlight"`
	Delayed     int64  `json:"delayed"`
	OldestAt    int64  `json:"oldestAt,omitempty"`
	Consumer    string `json:"consumer,omitempty"`
	ConsumedAt  int64  `json:"consumedAt,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	seq         int64
}

// QueueMessageIn is one message to send.
type QueueMessageIn struct {
	Body         []byte `json:"body"`
	ContentType  string `json:"contentType,omitempty"`
	DelaySeconds *int   `json:"delaySeconds,omitempty"`
}

// QueueSendResult is POST …/messages' answer.
type QueueSendResult struct {
	IDs      []string `json:"ids"`
	Backlog  int64    `json:"backlog"`
	Bytes    int64    `json:"bytes"`
	OldestAt int64    `json:"oldestAt,omitempty"`
}

// QueueMessage is one message as delivered (pull) or inspected (peek).
type QueueMessage struct {
	ID          string `json:"id"`
	LeaseID     string `json:"leaseId,omitempty"`
	Body        []byte `json:"body"`
	ContentType string `json:"contentType"`
	Attempts    int64  `json:"attempts"`
	Timestamp   int64  `json:"timestamp"`
	VisibleAt   int64  `json:"visibleAt,omitempty"`
}

// QueuePullOpts is POST …/messages/pull's body.
type QueuePullOpts struct {
	BatchSize           int    `json:"batchSize,omitempty"`
	VisibilityTimeoutMs int64  `json:"visibilityTimeoutMs,omitempty"`
	WaitMs              int64  `json:"waitMs,omitempty"`
	MaxRetries          *int   `json:"maxRetries,omitempty"`
	DeadLetterQueue     string `json:"deadLetterQueue,omitempty"`
}

// QueuePullResult is POST …/messages/pull's answer.
type QueuePullResult struct {
	Messages     []QueueMessage `json:"messages"`
	DeadLettered int            `json:"deadLettered,omitempty"`
}

// QueueRetryIn is one retry of an ack call.
type QueueRetryIn struct {
	LeaseID      string `json:"leaseId"`
	DelaySeconds *int   `json:"delaySeconds,omitempty"`
}

// QueueAckReq is POST …/messages/ack's body.
type QueueAckReq struct {
	Acks                []string       `json:"acks,omitempty"`
	Retries             []QueueRetryIn `json:"retries,omitempty"`
	Extend              []string       `json:"extend,omitempty"`
	Release             []string       `json:"release,omitempty"`
	VisibilityTimeoutMs int64          `json:"visibilityTimeoutMs,omitempty"`
	RetryDelaySeconds   *int           `json:"retryDelaySeconds,omitempty"`
	MaxRetries          *int           `json:"maxRetries,omitempty"`
	DeadLetterQueue     string         `json:"deadLetterQueue,omitempty"`
}

// QueueCaller is who calls a queue operation: an admin (Workload ""), or
// a host acting for Workload at Epoch.
type QueueCaller struct {
	HostID   string
	Workload string
	Epoch    *int64
	By       string // audit actor
}

func (m *Mgmt) queueStore() (queueStore, error) {
	qs, ok := m.store.(queueStore)
	if !ok {
		return nil, errf(501, "internal", "this mgmt store has no queues")
	}
	return qs, nil
}

func checkQueueAddr(tenantID, name string) error {
	if tenantID == "" || strings.ContainsAny(tenantID, "/\x00") {
		return errf(400, "bad_request", "bad tenant ID")
	}
	if !queuecfg.ValidName(name) {
		return errf(400, "bad_request", "bad queue name %q (lowercase letters, digits and dashes, at most 63)", name)
	}
	return nil
}

// ---- deployments' declarations ----

// queueDecls caches each deployment's parsed queues config.
var queueDecls sync.Map // deployment ID -> *queuecfg.Config (nil: none)

// BundleQueues returns the bundle's wrangler "queues" config, validated
// (nil: none). Hosts re-validate on deploy.
func BundleQueues(bundle []byte) (*queuecfg.Config, error) {
	files, err := bundleFiles(bundle, "wrangler.jsonc", "wrangler.json")
	if err != nil {
		return nil, err
	}
	for _, f := range []string{"wrangler.jsonc", "wrangler.json"} {
		b, ok := files[f]
		if !ok {
			continue
		}
		std, err := hujson.Standardize(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		c, err := queuecfg.FromWrangler(std)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		return c, nil
	}
	return nil, nil
}

// workloadQueues is the queues config of a workload's latest deployment.
func (m *Mgmt) workloadQueues(ctx context.Context, tenantID, workload string) (*queuecfg.Config, error) {
	d, err := m.latestDeployment(ctx, tenantID, workload)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c, ok := queueDecls.Load(d.ID); ok {
		return c.(*queuecfg.Config), nil
	}
	if len(d.Bundle) == 0 {
		if d, err = m.store.GetDeployment(ctx, d.ID); err != nil {
			return nil, err
		}
	}
	c, err := BundleQueues(d.Bundle)
	if err != nil {
		return nil, err
	}
	queueDecls.Store(d.ID, c)
	return c, nil
}

// queueRole is what a caller wants of a queue.
type queueRole int

const (
	queueAsProducer queueRole = iota
	queueAsConsumer
	queueAsEither
	queueAsReleaser // a consumer handing back a claim, possibly from an older epoch (queuestalerelease.go)
)

// authorizeQueue fences a host caller to its workload's lease and checks
// that the workload's deployment declares the queue for role. It returns
// the workload's consumer settings (nil for admins and producers).
func (m *Mgmt) authorizeQueue(ctx context.Context, tenantID, name string, c QueueCaller, role queueRole) (*queuecfg.Consumer, error) {
	if err := checkQueueAddr(tenantID, name); err != nil {
		return nil, err
	}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return nil, lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	if c.Workload == "" {
		if principalOf(ctx).Kind == "host" || c.HostID != "" {
			return nil, errf(400, "bad_request", "a host must give the workload it acts for and its lease epoch")
		}
		return nil, nil // an admin
	}
	if !validName(c.Workload) {
		return nil, errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if c.Epoch == nil {
		return nil, errf(400, "bad_request", "a host must give the epoch of the lease it holds")
	}
	fence := m.fenceStore
	if role == queueAsReleaser {
		fence = m.fenceStaleRelease
	}
	if err := fence(ctx, tenantID, c.Workload, c.HostID, c.Epoch, "queue/"+name); err != nil {
		return nil, err
	}
	decl, err := m.workloadQueues(ctx, tenantID, c.Workload)
	if err != nil {
		return nil, err
	}
	p, k := decl.ProducerOf(name), decl.ConsumerOf(name)
	if k != nil && !k.Push() {
		k = nil // an http_pull consumer runs nowhere on a host
	}
	switch {
	case role == queueAsProducer && p != nil, role == queueAsEither && (p != nil || k != nil):
		return k, nil
	case (role == queueAsConsumer || role == queueAsReleaser) && k != nil:
		return k, nil
	}
	what := map[queueRole]string{queueAsProducer: "a producer binding of", queueAsConsumer: "a consumer of", queueAsEither: "a producer binding or consumer of", queueAsReleaser: "a consumer of"}[role]
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "deny", Code: "forbidden"},
		map[string]any{"host": c.HostID, "workload": c.Workload, "queue": name, "want": what})
	return nil, errf(403, "forbidden", "workload %s does not declare %s queue %s (wrangler.jsonc queues; redeploy to change)", c.Workload, what, name)
}

// ---- data keys ----

var queueKeys sync.Map // incarnation -> []byte

func queueKeyCustodyName(tenantID, inc string) string {
	return "tw/" + tenantID + "/queues/" + inc + "/key"
}

func (m *Mgmt) queueKey(ctx context.Context, tenantID, inc string) ([]byte, error) {
	if k, ok := queueKeys.Load(inc); ok {
		return k.([]byte), nil
	}
	k, err := m.custody.Get(ctx, queueKeyCustodyName(tenantID, inc))
	if err != nil {
		return nil, fmt.Errorf("custody: queue key: %w", err)
	}
	if len(k) != 32 {
		return nil, errors.New("custody: queue key: want 32 bytes")
	}
	queueKeys.Store(inc, k)
	return k, nil
}

func queueAAD(inc, id string) []byte { return []byte("tw-queue-v1\n" + inc + "\n" + id) }

func sealQueueBody(key []byte, inc, id string, body []byte) ([]byte, error) {
	g, err := storeAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, body, queueAAD(inc, id)), nil
}

func openQueueBody(key []byte, inc, id string, sealed []byte) ([]byte, error) {
	g, err := storeAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("queue: truncated message")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], queueAAD(inc, id))
	if err != nil {
		return nil, errors.New("queue: message failed authentication")
	}
	return out, nil
}

// ---- long-poll wakeups ----

type queueWaker struct {
	mu sync.Mutex
	ch chan struct{}
}

var queueWakers sync.Map // tenant + "/" + name -> *queueWaker

func queueWakeKey(tenantID, name string) string { return tenantID + "/" + name }

// queueWaitCh is closed at the queue's next send (in this process).
func queueWaitCh(key string) <-chan struct{} {
	v, _ := queueWakers.LoadOrStore(key, &queueWaker{ch: make(chan struct{})})
	w := v.(*queueWaker)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

func wakeQueue(key string) {
	v, ok := queueWakers.Load(key)
	if !ok {
		return
	}
	w := v.(*queueWaker)
	w.mu.Lock()
	close(w.ch)
	w.ch = make(chan struct{})
	w.mu.Unlock()
}

// ---- operations ----

// ensureQueue returns the queue, creating it (and its key) if it doesn't
// exist. created reports whether this call created it.
func (m *Mgmt) ensureQueue(ctx context.Context, qs queueStore, tenantID, name, by string) (QueueInfo, bool, error) {
	if q, err := qs.GetQueue(ctx, tenantID, name); err == nil {
		return q, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return QueueInfo{}, false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return QueueInfo{}, false, err
	}
	now := m.now().UnixMilli()
	q := QueueInfo{TenantID: tenantID, Name: name, Incarnation: newID("qi_"), CreatedAt: now, UpdatedAt: now}
	if err := m.custody.Put(ctx, queueKeyCustodyName(tenantID, q.Incarnation), key); err != nil {
		return QueueInfo{}, false, fmt.Errorf("custody: %w", err)
	}
	created, err := qs.CreateQueue(ctx, q)
	if err != nil || !created {
		m.custody.Delete(ctx, queueKeyCustodyName(tenantID, q.Incarnation))
		if err != nil {
			return QueueInfo{}, false, err
		}
		q, err = qs.GetQueue(ctx, tenantID, name) // a concurrent creator won
		return q, false, err
	}
	queueKeys.Store(q.Incarnation, key)
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "queue_create", ActingFor: by},
		map[string]any{"queue": name, "incarnation": q.Incarnation, "by": by})
	return q, true, nil
}

// CreateQueue creates a queue (created=false: it existed).
func (m *Mgmt) CreateQueue(ctx context.Context, tenantID, name, by string) (QueueInfo, bool, error) {
	qs, err := m.queueStore()
	if err != nil {
		return QueueInfo{}, false, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, QueueCaller{}, queueAsEither); err != nil {
		return QueueInfo{}, false, err
	}
	q, created, err := m.ensureQueue(ctx, qs, tenantID, name, by)
	if err != nil {
		return q, false, err
	}
	return q, created, qs.QueueStats(ctx, &q, m.now().UnixMilli())
}

// GetQueue returns a queue and its counters.
func (m *Mgmt) GetQueue(ctx context.Context, tenantID, name string, c QueueCaller) (QueueInfo, error) {
	qs, err := m.queueStore()
	if err != nil {
		return QueueInfo{}, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, c, queueAsEither); err != nil {
		return QueueInfo{}, err
	}
	q, err := qs.GetQueue(ctx, tenantID, name)
	if err != nil {
		return q, lookupErr(err, errf(404, "unknown_queue", "tenant has no queue %q", name))
	}
	return q, qs.QueueStats(ctx, &q, m.now().UnixMilli())
}

// ListQueues lists a tenant's queues with their counters.
func (m *Mgmt) ListQueues(ctx context.Context, tenantID string, pg Page) ([]QueueInfo, error) {
	qs, err := m.queueStore()
	if err != nil {
		return nil, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, "x", QueueCaller{}, queueAsEither); err != nil {
		return nil, err
	}
	out, err := qs.ListQueues(ctx, tenantID, pg)
	if err != nil {
		return nil, err
	}
	now := m.now().UnixMilli()
	for i := range out {
		if err := qs.QueueStats(ctx, &out[i], now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// DeleteQueue deletes a queue, its messages and its data key.
func (m *Mgmt) DeleteQueue(ctx context.Context, tenantID, name, by string) error {
	qs, err := m.queueStore()
	if err != nil {
		return err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, QueueCaller{}, queueAsEither); err != nil {
		return err
	}
	q, err := qs.DeleteQueue(ctx, tenantID, name)
	if err != nil {
		return lookupErr(err, errf(404, "unknown_queue", "tenant has no queue %q", name))
	}
	queueKeys.Delete(q.Incarnation)
	if err := m.custody.Delete(ctx, queueKeyCustodyName(tenantID, q.Incarnation)); err != nil {
		m.log.Warn("queue deleted, but its data key is still in custody", "incarnation", q.Incarnation, "err", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "queue_delete", ActingFor: by},
		map[string]any{"queue": name, "incarnation": q.Incarnation, "messages": q.Backlog, "by": by})
	return nil
}

// PurgeQueue deletes every message of a queue.
func (m *Mgmt) PurgeQueue(ctx context.Context, tenantID, name, by string) (int64, error) {
	qs, err := m.queueStore()
	if err != nil {
		return 0, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, QueueCaller{}, queueAsEither); err != nil {
		return 0, err
	}
	n, err := qs.PurgeQueue(ctx, tenantID, name, m.now().UnixMilli())
	if err != nil {
		return 0, lookupErr(err, errf(404, "unknown_queue", "tenant has no queue %q", name))
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "queue_purge", ActingFor: by},
		map[string]any{"queue": name, "purged": n, "by": by})
	return n, nil
}

func delayMs(d *int, def int) (int64, error) {
	v := def
	if d != nil {
		v = *d
	}
	if v < 0 || v > queuecfg.MaxDelaySeconds {
		return 0, errf(400, "queue_limit", "a delay is 0 to %d seconds", queuecfg.MaxDelaySeconds)
	}
	return int64(v) * 1000, nil
}

// SendQueue sends messages (defaultDelay applies to those without their
// own delaySeconds).
func (m *Mgmt) SendQueue(ctx context.Context, tenantID, name string, c QueueCaller, msgs []QueueMessageIn, defaultDelay *int) (QueueSendResult, error) {
	qs, err := m.queueStore()
	if err != nil {
		return QueueSendResult{}, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, c, queueAsProducer); err != nil {
		return QueueSendResult{}, err
	}
	if len(msgs) == 0 || len(msgs) > queuecfg.MaxBatchMessages {
		return QueueSendResult{}, errf(400, "queue_limit", "a send has 1 to %d messages", queuecfg.MaxBatchMessages)
	}
	def := 0
	if defaultDelay != nil {
		if _, err := delayMs(defaultDelay, 0); err != nil {
			return QueueSendResult{}, err
		}
		def = *defaultDelay
	}
	total := 0
	for i, msg := range msgs {
		if len(msg.Body) > queuecfg.MaxMessageBytes {
			return QueueSendResult{}, errf(400, "queue_limit", "a message is at most %d bytes (message %d has %d)", queuecfg.MaxMessageBytes, i, len(msg.Body))
		}
		if msg.ContentType == "" {
			msgs[i].ContentType = "json"
		} else if !queuecfg.ValidContentType(msg.ContentType) {
			return QueueSendResult{}, errf(400, "bad_request", "unknown contentType %q (json, text, bytes, v8)", msg.ContentType)
		}
		if msgs[i].ContentType == "json" && !json.Valid(msg.Body) {
			return QueueSendResult{}, errf(400, "bad_request", "message %d: contentType json, but the body is not JSON", i)
		}
		total += len(msg.Body)
	}
	if total > queuecfg.MaxBatchBytes {
		return QueueSendResult{}, errf(400, "queue_limit", "a send is at most %d bytes (got %d)", queuecfg.MaxBatchBytes, total)
	}
	by := c.Workload
	if by == "" {
		by = c.By
	}
	for attempt := 0; ; attempt++ {
		q, _, err := m.ensureQueue(ctx, qs, tenantID, name, by)
		if err != nil {
			return QueueSendResult{}, err
		}
		key, err := m.queueKey(ctx, tenantID, q.Incarnation)
		if err != nil {
			return QueueSendResult{}, err
		}
		now := m.now().UnixMilli()
		rows := make([]queueMsgRow, len(msgs))
		res := QueueSendResult{IDs: make([]string, len(msgs))}
		for i, msg := range msgs {
			d, err := delayMs(msg.DelaySeconds, def)
			if err != nil {
				return QueueSendResult{}, err
			}
			id := newID("qm_")
			sealed, err := sealQueueBody(key, q.Incarnation, id, msg.Body)
			if err != nil {
				return QueueSendResult{}, err
			}
			rows[i] = queueMsgRow{ID: id, ContentType: msg.ContentType, Size: int64(len(msg.Body)), Body: sealed, SentAt: now, VisibleAt: now + d}
			res.IDs[i] = id
		}
		quota, err := m.tenantQuotas(ctx, tenantID) // quota.go
		if err != nil {
			return QueueSendResult{}, err
		}
		q, err = qs.SendQueue(ctx, tenantID, name, q.Incarnation, rows, QueueMaxBytes, quota.MaxQueueBytes, now)
		if qe, ok := asQuotaError(err, tenantID, "queue "+name); ok {
			return QueueSendResult{}, qe
		}
		switch {
		case errors.Is(err, errQueueGone) && attempt < 2:
			continue // deleted under us: send to the new queue
		case errors.Is(err, errQueueFull):
			return QueueSendResult{}, errf(400, "queue_limit", "queue %s would exceed %d bytes", name, QueueMaxBytes)
		case err != nil:
			return QueueSendResult{}, err
		}
		wakeQueue(queueWakeKey(tenantID, name))
		if err := qs.QueueStats(ctx, &q, now); err != nil {
			return QueueSendResult{}, err
		}
		res.Backlog, res.Bytes, res.OldestAt = q.Backlog, q.Bytes, q.OldestAt
		return res, nil
	}
}

// consumerSettings are the retry settings a claim or settle applies.
type consumerSettings struct {
	maxRetries int
	dlq        string
	retryDelay int // seconds
}

func settingsOf(k *queuecfg.Consumer, maxRetries *int, dlq string, retryDelay *int) (consumerSettings, error) {
	if k != nil {
		return consumerSettings{maxRetries: k.Retries(), dlq: k.DeadLetterQueue, retryDelay: k.RetryDelay}, nil
	}
	s := consumerSettings{maxRetries: queuecfg.DefaultMaxRetries, dlq: dlq}
	if maxRetries != nil {
		if *maxRetries < 0 || *maxRetries > queuecfg.MaxRetries {
			return s, errf(400, "bad_request", "maxRetries is 0 to %d", queuecfg.MaxRetries)
		}
		s.maxRetries = *maxRetries
	}
	if retryDelay != nil {
		if *retryDelay < 0 || *retryDelay > queuecfg.MaxDelaySeconds {
			return s, errf(400, "bad_request", "retryDelaySeconds is 0 to %d", queuecfg.MaxDelaySeconds)
		}
		s.retryDelay = *retryDelay
	}
	if dlq != "" && !queuecfg.ValidName(dlq) {
		return s, errf(400, "bad_request", "bad deadLetterQueue %q", dlq)
	}
	return s, nil
}

// deadLetterTarget prepares the dead-letter queue of src (nil: none).
func (m *Mgmt) deadLetterTarget(ctx context.Context, qs queueStore, src QueueInfo, dlq, by string) (*queueTarget, error) {
	if dlq == "" {
		return nil, nil
	}
	if dlq == src.Name {
		return nil, errf(400, "bad_request", "a queue can't be its own dead-letter queue")
	}
	d, _, err := m.ensureQueue(ctx, qs, src.TenantID, dlq, by)
	if err != nil {
		return nil, err
	}
	srcKey, err := m.queueKey(ctx, src.TenantID, src.Incarnation)
	if err != nil {
		return nil, err
	}
	dKey, err := m.queueKey(ctx, d.TenantID, d.Incarnation)
	if err != nil {
		return nil, err
	}
	return &queueTarget{TenantID: d.TenantID, Name: d.Name, Incarnation: d.Incarnation,
		Reseal: func(id string, sealed []byte) ([]byte, error) {
			plain, err := openQueueBody(srcKey, src.Incarnation, id, sealed)
			if err != nil {
				return nil, err
			}
			return sealQueueBody(dKey, d.Incarnation, id, plain)
		}}, nil
}

func newLeaseToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func visibilityMs(v int64) (int64, error) {
	if v == 0 {
		return queueDefaultVisibility, nil
	}
	if v < 1000 || v > queuecfg.MaxVisibilityMillis {
		return 0, errf(400, "bad_request", "visibilityTimeoutMs is 1000 to %d", queuecfg.MaxVisibilityMillis)
	}
	return v, nil
}

// PullQueue claims a batch (see the package comment).
func (m *Mgmt) PullQueue(ctx context.Context, tenantID, name string, c QueueCaller, o QueuePullOpts) (QueuePullResult, error) {
	qs, err := m.queueStore()
	if err != nil {
		return QueuePullResult{}, err
	}
	k, err := m.authorizeQueue(ctx, tenantID, name, c, queueAsConsumer)
	if err != nil {
		return QueuePullResult{}, err
	}
	set, err := settingsOf(k, o.MaxRetries, o.DeadLetterQueue, nil)
	if err != nil {
		return QueuePullResult{}, err
	}
	if o.BatchSize == 0 {
		o.BatchSize = queuecfg.DefaultBatchSize
	}
	if o.BatchSize < 0 || o.BatchSize > queuecfg.MaxBatchMessages {
		return QueuePullResult{}, errf(400, "bad_request", "batchSize is 1 to %d", queuecfg.MaxBatchMessages)
	}
	vis, err := visibilityMs(o.VisibilityTimeoutMs)
	if err != nil {
		return QueuePullResult{}, err
	}
	if o.WaitMs < 0 || time.Duration(o.WaitMs)*time.Millisecond > queueMaxWait {
		return QueuePullResult{}, errf(400, "bad_request", "waitMs is 0 to %d", queueMaxWait.Milliseconds())
	}
	consumer := c.Workload
	if consumer == "" {
		consumer = "admin"
	}
	q, _, err := m.ensureQueue(ctx, qs, tenantID, name, consumer)
	if err != nil {
		return QueuePullResult{}, err
	}
	dlq, err := m.deadLetterTarget(ctx, qs, q, set.dlq, consumer)
	if err != nil {
		return QueuePullResult{}, err
	}
	key, err := m.queueKey(ctx, tenantID, q.Incarnation)
	if err != nil {
		return QueuePullResult{}, err
	}
	deadline := time.Now().Add(time.Duration(o.WaitMs) * time.Millisecond)
	wk := queueWakeKey(tenantID, name)
	res := QueuePullResult{Messages: []QueueMessage{}}
	for {
		wait := queueWaitCh(wk) // before the claim, so a send in between wakes us
		rows, dead, err := qs.ClaimQueue(ctx, tenantID, name, q.Incarnation, queueClaim{
			Max: o.BatchSize, Now: m.now().UnixMilli(), VisibilityMs: vis, MaxRetries: int64(set.maxRetries),
			DLQ: dlq, Consumer: consumer, NewLease: newLeaseToken,
		})
		if err != nil {
			return QueuePullResult{}, err
		}
		res.DeadLettered += dead
		if dead > 0 && dlq != nil {
			wakeQueue(queueWakeKey(tenantID, dlq.Name))
		}
		if len(rows) > 0 {
			if ctx.Err() != nil { // the caller left while the claim committed (queueclaimgone.go)
				m.releaseUndelivered(tenantID, name, claimLeases(rows))
				return res, nil
			}
			for _, r := range rows {
				body, err := openQueueBody(key, q.Incarnation, r.ID, r.Body)
				if err != nil {
					return QueuePullResult{}, fmt.Errorf("queue %s message %s: %w", name, r.ID, err)
				}
				res.Messages = append(res.Messages, QueueMessage{ID: r.ID, LeaseID: r.ID + ":" + r.Lease, Body: body,
					ContentType: r.ContentType, Attempts: r.Attempts, Timestamp: r.SentAt})
			}
			return res, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return res, nil
		}
		// Re-check at least every second: sends through another mgmt
		// replica, and delays that expire, don't wake this process.
		t := time.NewTimer(min(left, time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return res, nil
		case <-wait:
			t.Stop()
		case <-t.C:
		}
	}
}

// AckQueue settles claimed messages.
func (m *Mgmt) AckQueue(ctx context.Context, tenantID, name string, c QueueCaller, a QueueAckReq) (QueueAckResult, error) {
	qs, err := m.queueStore()
	if err != nil {
		return QueueAckResult{}, err
	}
	k, err := m.authorizeQueue(ctx, tenantID, name, c, ackRole(a))
	if err != nil {
		return QueueAckResult{}, err
	}
	set, err := settingsOf(k, a.MaxRetries, a.DeadLetterQueue, a.RetryDelaySeconds)
	if err != nil {
		return QueueAckResult{}, err
	}
	n := queuecfg.MaxBatchMessages
	if len(a.Acks) > n || len(a.Retries) > n || len(a.Extend) > n || len(a.Release) > n {
		return QueueAckResult{}, errf(400, "bad_request", "at most %d acks, retries, extends and releases each", n)
	}
	vis, err := visibilityMs(a.VisibilityTimeoutMs)
	if err != nil {
		return QueueAckResult{}, err
	}
	st := queueSettle{Acks: a.Acks, Extend: a.Extend, Release: a.Release, Now: m.now().UnixMilli(), VisibilityMs: vis, MaxRetries: int64(set.maxRetries)}
	for _, r := range a.Retries {
		d, err := delayMs(r.DelaySeconds, set.retryDelay)
		if err != nil {
			return QueueAckResult{}, err
		}
		st.Retries = append(st.Retries, queueRetry{Lease: r.LeaseID, DelayMs: d})
	}
	q, err := qs.GetQueue(ctx, tenantID, name)
	if errors.Is(err, ErrNotFound) {
		// Deleted: every lease is stale.
		return QueueAckResult{Stale: len(a.Acks) + len(a.Retries) + len(a.Extend) + len(a.Release)}, nil
	}
	if err != nil {
		return QueueAckResult{}, err
	}
	if len(a.Retries) > 0 {
		by := c.Workload
		if by == "" {
			by = "admin"
		}
		if st.DLQ, err = m.deadLetterTarget(ctx, qs, q, set.dlq, by); err != nil {
			return QueueAckResult{}, err
		}
	}
	res, err := qs.SettleQueue(ctx, tenantID, name, q.Incarnation, st)
	if err != nil {
		return res, err
	}
	if res.Retried > 0 || res.Released > 0 {
		wakeQueue(queueWakeKey(tenantID, name))
	}
	if res.DeadLettered > 0 && st.DLQ != nil {
		wakeQueue(queueWakeKey(tenantID, st.DLQ.Name))
	}
	return res, nil
}

// PeekQueue returns up to limit messages without claiming them.
func (m *Mgmt) PeekQueue(ctx context.Context, tenantID, name string, limit int) ([]QueueMessage, error) {
	qs, err := m.queueStore()
	if err != nil {
		return nil, err
	}
	if _, err := m.authorizeQueue(ctx, tenantID, name, QueueCaller{}, queueAsEither); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = queuePeekDefault
	}
	if limit > queuecfg.MaxBatchMessages {
		return nil, errf(400, "bad_request", "limit is 1 to %d", queuecfg.MaxBatchMessages)
	}
	q, err := qs.GetQueue(ctx, tenantID, name)
	if err != nil {
		return nil, lookupErr(err, errf(404, "unknown_queue", "tenant has no queue %q", name))
	}
	key, err := m.queueKey(ctx, tenantID, q.Incarnation)
	if err != nil {
		return nil, err
	}
	rows, err := qs.PeekQueue(ctx, q.Incarnation, limit)
	if err != nil {
		return nil, err
	}
	out := make([]QueueMessage, 0, len(rows))
	for _, r := range rows {
		body, err := openQueueBody(key, q.Incarnation, r.ID, r.Body)
		if err != nil {
			return nil, fmt.Errorf("queue %s message %s: %w", name, r.ID, err)
		}
		out = append(out, QueueMessage{ID: r.ID, Body: body, ContentType: r.ContentType, Attempts: r.Attempts, Timestamp: r.SentAt, VisibleAt: r.VisibleAt})
	}
	return out, nil
}

// ---- /v1 handlers ----

func queueCaller(r *http.Request, workload *string, epoch *int64) QueueCaller {
	return QueueCaller{HostID: hostOf(r), Workload: str(workload), Epoch: epoch, By: principalOf(r.Context()).Actor(r)}
}

func (a *v1api) ListQueues(w http.ResponseWriter, r *http.Request, id v1.TenantID, params v1.ListQueuesParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rows, err := a.s.M.ListQueues(r.Context(), id, pg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, mkPage(rows, n, func(q QueueInfo) PageKey { return PageKey{ID: q.Name} }))
}

func (a *v1api) GetQueue(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName, params v1.GetQueueParams) {
	q, err := a.s.M.GetQueue(r.Context(), id, queue, queueCaller(r, params.Workload, params.Epoch))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, q)
}

func (a *v1api) PutQueue(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName) {
	q, created, err := a.s.M.CreateQueue(r.Context(), id, queue, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, q)
}

func (a *v1api) DeleteQueue(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName) {
	if err := a.s.M.DeleteQueue(r.Context(), id, queue, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) PurgeQueue(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName) {
	n, err := a.s.M.PurgeQueue(r.Context(), id, queue, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"purged": n})
}

func (a *v1api) PeekQueueMessages(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName, params v1.PeekQueueMessagesParams) {
	w.Header().Set("Cache-Control", "no-store")
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	msgs, err := a.s.M.PeekQueue(r.Context(), id, queue, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs})
}

func (a *v1api) SendQueueMessages(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName, params v1.SendQueueMessagesParams) {
	var body struct {
		Messages     []QueueMessageIn `json:"messages"`
		DelaySeconds *int             `json:"delaySeconds"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.SendQueue(r.Context(), id, queue, queueCaller(r, params.Workload, params.Epoch), body.Messages, body.DelaySeconds)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, res)
}

func (a *v1api) PullQueueMessages(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName, params v1.PullQueueMessagesParams) {
	w.Header().Set("Cache-Control", "no-store")
	var o QueuePullOpts
	if err := readJSON(r, &o); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.PullQueue(r.Context(), id, queue, queueCaller(r, params.Workload, params.Epoch), o)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	a.s.M.writePulled(w, id, queue, res) // queueclaimgone.go
}

func (a *v1api) AckQueueMessages(w http.ResponseWriter, r *http.Request, id v1.TenantID, queue v1.QueueName, params v1.AckQueueMessagesParams) {
	var body QueueAckReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := a.s.M.AckQueue(r.Context(), id, queue, queueCaller(r, params.Workload, params.Epoch), body)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}
