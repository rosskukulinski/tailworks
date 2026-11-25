package hostd

// Queues (ADR-039), host side.
//
// Producers: the platform's QueueProducer (gen/js/queue.js) reaches mgmt's
// queues only through hostd's local host API:
//
//	POST /v1/queue/send    {queue, binding, messages: [{body, contentType, delaySeconds}]}
//	POST /v1/queue/metrics {queue, binding}
//
// hostd checks that the running deployment binds the queue as a producer,
// applies the binding's delivery_delay, and sends as the workload's lease
// holder at its epoch (mgmt fences it and checks the declaration again).
//
// Consumers: for every push consumer (wrangler queues.consumers) of every
// active workload on this host, while this host's heartbeat is fresh (the
// rule cron triggers follow), hostd runs max_concurrency loops that claim
// a batch from mgmt (long poll; then up to max_batch_size or
// max_batch_timeout), run the default export's queue() handler over the
// instance's private ingress (cf.tw kind "queue", which only hostd can
// set), and settle the batch: explicit acks and retries as the handler
// made them, the rest acked if it returned and retried if it threw. The
// leases are extended while the handler runs. Nothing is kept here: a
// crash, restart or lease move leaves the messages in mgmt, where their
// leases expire and another claim gets them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/queuecfg"
	"github.com/rosskukulinski/tailworks/internal/wallclock"
	"github.com/rosskukulinski/tailworks/netd"
)

const (
	// DefaultQueueVisibility is how long a claimed batch stays hidden in
	// mgmt before another claim may take it (hostd extends it while the
	// handler runs, so it bounds recovery after a crash, not the handler).
	DefaultQueueVisibility = 60 * time.Second
	// DefaultQueueTimeout bounds one queue() run (Cloudflare's consumer
	// wall-time limit is 15 minutes).
	DefaultQueueTimeout = 15 * time.Minute
	queuePollWait       = 10 * time.Second
	queueReconcileEvery = 500 * time.Millisecond
)

// ---- producers: the platform's local API ----

type queueAPIReq struct {
	Queue    string                   `json:"queue"`
	Binding  string                   `json:"binding"`
	Messages []mgmtapi.QueueMessageIn `json:"messages"`
}

var queueErrStatus = map[string]int{
	"bad_request":            400,
	"queue_limit":            400,
	"storage_quota_exceeded": 429,
	"forbidden":              403,
	"unknown_queue":          404,
	mgmtapi.CodeLeaseLost:    409,
}

func writeQueueError(w http.ResponseWriter, e *apiError) {
	st := queueErrStatus[e.Code]
	if st == 0 {
		e = &apiError{Code: "queue_unavailable", Detail: e.Code + ": " + e.Detail}
		st = 503
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(st)
	json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "message": e.Detail})
}

// queueAPI serves /v1/queue/* for inst's platform.
func (h *Host) queueAPI(inst *instance, w http.ResponseWriter, r *http.Request) {
	var req queueAPIReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeQueueError(w, &apiError{Code: "bad_request", Detail: "queue: " + err.Error()})
		return
	}
	p := inst.app.Queues.ProducerBinding(req.Binding)
	if p == nil || p.Queue != req.Queue {
		writeQueueError(w, &apiError{Code: "forbidden", Detail: "queue " + req.Queue + " is not bound by this deployment"})
		return
	}
	wl := inst.w
	ctx := r.Context()
	switch strings.TrimPrefix(r.URL.Path, "/v1/queue/") {
	case "send":
		for i := range req.Messages {
			if req.Messages[i].DelaySeconds == nil && p.DeliveryDelay > 0 {
				d := p.DeliveryDelay
				req.Messages[i].DelaySeconds = &d
			}
		}
		res, err := h.mgmt.SendQueue(ctx, wl.tenantID, req.Queue, wl.name, wl.epochNow(), req.Messages)
		if err != nil {
			writeQueueError(w, asAPIError(err))
			return
		}
		writeJSON(w, 200, res)
	case "metrics":
		info, err := h.mgmt.QueueInfo(ctx, wl.tenantID, req.Queue, wl.name, wl.epochNow())
		if me, ok := mgmtapi.AsError(err); ok && me.Code == "unknown_queue" {
			writeJSON(w, 200, mgmtapi.QueueInfo{Name: req.Queue}) // nothing sent yet
			return
		}
		if err != nil {
			writeQueueError(w, asAPIError(err))
			return
		}
		writeJSON(w, 200, info)
	default:
		writeQueueError(w, &apiError{Code: "bad_request", Detail: "unknown queue op"})
	}
}

// ---- consumers ----

// queueRunKey names one consumer loop.
type queueRunKey struct {
	tenant, workload, deployment, queue string
	slot                                int
}

type queueRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *Host) queueVisibility() time.Duration {
	if h.cfg.QueueVisibility > 0 {
		return h.cfg.QueueVisibility
	}
	return DefaultQueueVisibility
}

// queueWanted lists the consumer loops this host should run now.
func (h *Host) queueWanted() map[queueRunKey]queuecfg.Consumer {
	want := map[queueRunKey]queuecfg.Consumer{}
	if !h.cronLeaseFresh() {
		return want // like cron: a host that may have lost mgmt consumes nothing
	}
	h.mu.Lock()
	wls := make([]*workload, 0, len(h.workloads))
	for _, w := range h.workloads {
		wls = append(wls, w)
	}
	h.mu.Unlock()
	for _, w := range wls {
		w.mu.Lock()
		inst := w.active
		if w.status == "active" && inst != nil && inst.app != nil && !inst.stopping.Load() {
			for _, k := range inst.app.Queues.PushConsumers() {
				for i := 0; i < k.Concurrency(); i++ {
					want[queueRunKey{w.tenantID, w.name, inst.depID, k.Queue, i}] = k
				}
			}
		}
		w.mu.Unlock()
	}
	return want
}

// queueLoop starts and stops the consumer loops as workloads come and go.
func (h *Host) queueLoop() {
	defer h.wg.Done()
	runs := map[queueRunKey]*queueRun{}
	stop := func(k queueRunKey) {
		r := runs[k]
		r.cancel()
		<-r.done
		delete(runs, k)
	}
	defer func() {
		for k := range runs {
			stop(k)
		}
	}()
	t := time.NewTicker(queueReconcileEvery)
	defer t.Stop()
	for {
		want := h.queueWanted()
		for k, r := range runs {
			select {
			case <-r.done: // ended by itself (a self-fence cancelled it): start again when wanted
				delete(runs, k)
				continue
			default:
			}
			if _, ok := want[k]; !ok {
				stop(k)
			}
		}
		keys := make([]queueRunKey, 0, len(want))
		for k := range want {
			if runs[k] == nil {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			ctx, cancel := context.WithCancel(h.consumeContext()) // queuefence.go
			r := &queueRun{cancel: cancel, done: make(chan struct{})}
			runs[k] = r
			go func(k queueRunKey, c queuecfg.Consumer) {
				defer close(r.done)
				h.consumeQueue(ctx, k, c)
			}(k, want[k])
		}
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Host) queueWorkload(tenant, name string) *workload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workloads[tenant+"/"+name]
}

// sleepCtx waits d or until ctx is done (false).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// consumeQueue is one consumer loop: claim, batch, run, settle.
func (h *Host) consumeQueue(ctx context.Context, k queueRunKey, c queuecfg.Consumer) {
	log := h.log.With("workload", k.workload, "tenant", k.tenant, "queue", k.queue)
	w := h.queueWorkload(k.tenant, k.workload)
	if w == nil {
		return
	}
	vis := h.queueVisibility()
	backoff := time.Second
	for ctx.Err() == nil {
		if !h.cronLeaseFresh() {
			return // no claim from a host that may have lost mgmt (queuefence.go)
		}
		batch, err := h.claimBatch(ctx, w, k, c, vis)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("queue claim failed", "err", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		if len(batch) == 0 {
			backoff = time.Second
			continue
		}
		if ran := h.runBatch(ctx, w, k, batch, vis, log); ran {
			backoff = time.Second
		} else if !sleepCtx(ctx, backoff) { // the instance is restarting or gone
			return
		} else {
			backoff = min(2*backoff, 30*time.Second)
		}
	}
}

// errNotRun: the batch never reached queue() (no instance, it refused the
// connection, or hostd itself cancelled the delivery), so its attempts
// don't count.
var errNotRun = errors.New("queue batch not run")

func notRun(err error) error { return fmt.Errorf("%w: %w", errNotRun, err) }

// invokeErr classifies a failed POST of a batch to the platform, made under
// ctx (the run's context, bounded by DefaultQueueTimeout). Cancellation by
// hostd (the consumer loop was stopped: a redeploy, a stale heartbeat or
// self-fence, shutdown) and a refused connection (workerd restarting) are
// errNotRun: queue() never finished, through no fault of its own, so the
// batch is handed back uncounted (#387). The time limit and anything else
// count as a failed run, which retries the batch.
func invokeErr(ctx context.Context, err error) error {
	var op *net.OpError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("queue() exceeded the %v limit", DefaultQueueTimeout)
	case ctx.Err() != nil:
		return notRun(fmt.Errorf("delivery cancelled: %w", err))
	case errors.As(err, &op) && op.Op == "dial":
		return notRun(err)
	}
	return err
}

// claimBatch long-polls for a first message, then fills the batch up to
// max_batch_size until max_batch_timeout after the first claim.
func (h *Host) claimBatch(ctx context.Context, w *workload, k queueRunKey, c queuecfg.Consumer, vis time.Duration) ([]mgmtapi.QueueMessage, error) {
	size := c.BatchSize()
	pull := func(n int, wait time.Duration) ([]mgmtapi.QueueMessage, error) {
		res, err := h.mgmt.PullQueue(ctx, k.tenant, k.queue, k.workload, w.epochNow(), mgmtapi.QueuePullOpts{
			BatchSize: n, VisibilityTimeoutMs: vis.Milliseconds(), WaitMs: wait.Milliseconds()})
		return res.Messages, err
	}
	batch, err := pull(size, queuePollWait)
	if err != nil || len(batch) == 0 {
		return batch, err
	}
	deadline := time.Now().Add(c.BatchTimeout())
	for len(batch) < size {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		more, err := pull(size-len(batch), min(left, queuePollWait))
		if err != nil {
			break // deliver what we hold; their leases are ours
		}
		batch = append(batch, more...)
	}
	return batch, nil
}

// QueueResult is one queue() run's outcome, as workerd reports it.
type QueueResult struct {
	Outcome    string `json:"outcome"`
	AckAll     bool   `json:"ackAll"`
	RetryBatch struct {
		Retry        bool `json:"retry"`
		DelaySeconds *int `json:"delaySeconds"`
	} `json:"retryBatch"`
	ExplicitAcks  []string `json:"explicitAcks"`
	RetryMessages []struct {
		MsgID        string `json:"msgId"`
		DelaySeconds *int   `json:"delaySeconds"`
	} `json:"retryMessages"`
	Error string `json:"error,omitempty"`
}

// settlement turns a run's result into acks and retries. res nil: the run
// produced no result (workerd died, the instance went away): retry all.
func settlement(batch []mgmtapi.QueueMessage, res *QueueResult) mgmtapi.QueueAck {
	var a mgmtapi.QueueAck
	acked, retried := map[string]bool{}, map[string]*int{}
	ok := false
	if res != nil {
		ok = res.Outcome == "ok"
		for _, id := range res.ExplicitAcks {
			acked[id] = true
		}
		for _, r := range res.RetryMessages {
			retried[r.MsgID] = r.DelaySeconds
		}
	}
	for _, m := range batch {
		switch d, explicitRetry := retried[m.ID]; {
		case explicitRetry:
			a.Retries = append(a.Retries, mgmtapi.QueueRetry{LeaseID: m.LeaseID, DelaySeconds: d})
		case acked[m.ID]:
			a.Acks = append(a.Acks, m.LeaseID)
		case res != nil && res.RetryBatch.Retry:
			a.Retries = append(a.Retries, mgmtapi.QueueRetry{LeaseID: m.LeaseID, DelaySeconds: res.RetryBatch.DelaySeconds})
		case res != nil && (ok || res.AckAll):
			a.Acks = append(a.Acks, m.LeaseID)
		default:
			a.Retries = append(a.Retries, mgmtapi.QueueRetry{LeaseID: m.LeaseID})
		}
	}
	return a
}

// runBatch runs one batch through queue() and settles it. It reports
// whether queue() ran (false: the batch was handed back uncounted).
func (h *Host) runBatch(ctx context.Context, w *workload, k queueRunKey, batch []mgmtapi.QueueMessage, vis time.Duration, log *slog.Logger) bool {
	leases := make([]string, len(batch))
	for i, m := range batch {
		leases[i] = m.LeaseID
	}
	// Keep the batch hidden while the handler runs.
	ext, stopExt := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(vis / 3)
		defer t.Stop()
		for {
			select {
			case <-ext.Done():
				return
			case <-t.C:
				if _, err := h.mgmt.AckQueue(ext, k.tenant, k.queue, k.workload, w.epochNow(), mgmtapi.QueueAck{Extend: leases, VisibilityTimeoutMs: vis.Milliseconds()}); err != nil && ext.Err() == nil {
					log.Warn("queue lease extension failed", "err", err)
				}
			}
		}
	}()
	st := time.Now()
	res, err := h.invokeQueue(ctx, w, k, batch)
	stopExt()
	wg.Wait()
	var rp *QueueResult
	ran := !errors.Is(err, errNotRun)
	switch {
	case !ran:
		log.Warn("queue batch not delivered; handing it back", "messages", len(batch), "err", err)
	case err != nil:
		log.Warn("queue batch failed", "messages", len(batch), "err", err, "ms", time.Since(st).Milliseconds())
	case res.Outcome != "ok":
		rp = &res
		log.Warn("queue batch ran", "messages", len(batch), "outcome", res.Outcome, "error", res.Error, "ms", time.Since(st).Milliseconds())
	default:
		rp = &res
		log.Info("queue batch ran", "messages", len(batch), "outcome", res.Outcome, "ms", time.Since(st).Milliseconds())
	}
	a := settlement(batch, rp)
	if !ran {
		a = mgmtapi.QueueAck{Release: leases}
	}
	// Settle even if ctx was cancelled (a redeploy): the run happened. If
	// mgmt can't be reached, the leases expire and the messages come back.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		r, err := h.mgmt.AckQueue(sctx, k.tenant, k.queue, k.workload, w.epochNow(), a)
		if err == nil {
			if r.Stale > 0 || r.DeadLettered > 0 {
				log.Warn("queue batch settled", "acked", r.Acked, "retried", r.Retried, "deadLettered", r.DeadLettered, "stale", r.Stale)
			}
			return ran
		}
		if me, ok := mgmtapi.AsError(err); ok && me.Status/100 == 4 {
			log.Warn("queue settle refused", "err", err)
			return ran
		}
		if !sleepCtx(sctx, time.Second) {
			break
		}
	}
	log.Warn("queue settle failed; the batch will be delivered again when its leases expire")
	return ran
}

// RunQueueBatch claims and runs one batch of a consumer now (tw dev,
// tests); it reports how many messages it ran.
func (h *Host) RunQueueBatch(ctx context.Context, tenantID, workload, queue string) (int, error) {
	w := h.queueWorkload(tenantID, workload)
	if w == nil {
		return 0, &apiError{Code: "unknown_object", Detail: "workload " + workload + " is not hosted here"}
	}
	inst := w.current()
	if inst == nil || inst.app == nil {
		return 0, &apiError{Code: "upstream_unavailable", Detail: "workload " + workload + " has no running instance"}
	}
	c := inst.app.Queues.ConsumerOf(queue)
	if c == nil || !c.Push() {
		return 0, &apiError{Code: "bad_request", Detail: "workload " + workload + " has no consumer of queue " + queue}
	}
	k := queueRunKey{tenantID, workload, inst.depID, queue, -1}
	batch, err := h.claimBatch(ctx, w, k, *c, h.queueVisibility())
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	h.runBatch(ctx, w, k, batch, h.queueVisibility(), h.log.With("workload", workload, "queue", queue))
	return len(batch), nil
}

type queueWireMsg struct {
	ID          string `json:"id"`
	Timestamp   int64  `json:"timestamp"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
	Attempts    int64  `json:"attempts"`
}

// invokeQueue sends one batch to the workload's active instance (cf.tw
// kind "queue") and waits for its outcome.
func (h *Host) invokeQueue(ctx context.Context, w *workload, k queueRunKey, batch []mgmtapi.QueueMessage) (QueueResult, error) {
	var res QueueResult
	actx, cancel := context.WithTimeout(ctx, h.cfg.DrainTimeout+5*time.Second)
	inst, release := w.acquire(actx)
	cancel()
	if inst == nil {
		return res, notRun(errors.New("workload " + w.name + " has no running instance"))
	}
	defer release()
	if c := inst.app.Queues.ConsumerOf(k.queue); c == nil || !c.Push() {
		return res, notRun(fmt.Errorf("deployment %s has no consumer of queue %s", inst.depID, k.queue))
	}
	ctx, cancel = context.WithTimeout(ctx, DefaultQueueTimeout)
	defer cancel()
	msgs := make([]queueWireMsg, len(batch))
	for i, m := range batch {
		msgs[i] = queueWireMsg{m.ID, m.Timestamp, m.ContentType, m.Body, m.Attempts}
	}
	body, _ := json.Marshal(map[string]any{"messages": msgs})
	trace := newTraceID()
	blob, _ := json.Marshal(map[string]any{
		// The ticket lets queue() create objects through its guarded env.
		"tw":      map[string]any{"kind": "queue", "queue": k.queue, "workload": w.name, "ticket": gen.CreateTicket(inst.macKey(), trace, wallclock.Now())},
		"traceId": trace,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://platform/_tw/queue", strings.NewReader(string(body)))
	req.Header.Set(netd.HeaderCF, string(blob))
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Transport: inst.direct.Transport}).Do(req)
	if err != nil {
		return res, invokeErr(ctx, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return res, notRun(fmt.Errorf("platform refused the queue batch: %d %s", resp.StatusCode, netd.Redact(string(b))))
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return res, fmt.Errorf("bad platform response: %w", err)
	}
	res.Error = netd.Redact(res.Error)
	return res, nil
}
