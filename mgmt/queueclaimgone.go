package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// A claim nobody received (#412).
//
// A claim counts an attempt for every message it leases. If the caller is
// gone by the time the claim commits (hostd stopped the consumer loop: a
// stale lease, a self-fence, a redeploy, shutdown), or its response can't
// be written, nobody holds those leases. Left alone, they would expire
// after the visibility timeout as though a consumer had crashed with
// them: the messages would come back late, with a retry used up, and a
// message with max_retries 1 could reach its dead-letter queue without
// ever running. mgmt releases them instead. A release makes a message
// visible again at once and takes back the attempt its claim counted, as
// when a consumer hands back a batch it didn't run (#388).
//
// This doesn't let a fenced host claim (#140). Its pull is cancelled, and
// what the cancelled pull had claimed goes straight back to the queue.
//
// What's left: a response that was written in full but never read (the
// caller gave up in the moment after). mgmt can't tell that from a
// delivery, so those leases still expire.

// claimLeases is the lease IDs ("<id>:<token>") of claimed rows.
func claimLeases(rows []queueMsgRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID + ":" + r.Lease
	}
	return out
}

// releaseUndelivered releases leases of queue tenantID/name that no caller
// received, uncounted. It runs on its own context: the caller's is done.
func (m *Mgmt) releaseUndelivered(tenantID, name string, leases []string) {
	if len(leases) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := m.log.With("tenant", tenantID, "queue", name, "messages", len(leases))
	qs, err := m.queueStore()
	if err != nil {
		return
	}
	q, err := qs.GetQueue(ctx, tenantID, name)
	if err != nil {
		log.Warn("queue: a claim nobody received is not handed back; it will be delivered again when its leases expire", "err", err)
		return
	}
	res, err := qs.SettleQueue(ctx, tenantID, name, q.Incarnation, queueSettle{Release: leases, Now: m.now().UnixMilli()})
	if err != nil {
		log.Warn("queue: a claim nobody received is not handed back; it will be delivered again when its leases expire", "err", err)
		return
	}
	log.Info("queue: handed back a claim nobody received", "released", res.Released, "stale", res.Stale)
	if res.Released > 0 {
		wakeQueue(queueWakeKey(tenantID, name))
	}
}

// writePulled writes a pull's answer. If the body can't be written, the
// caller never got the batch, so its leases are released.
func (m *Mgmt) writePulled(w http.ResponseWriter, tenantID, name string, res QueuePullResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	err := json.NewEncoder(w).Encode(res)
	if err == nil {
		// Push the body out now, so a connection that is already gone
		// shows up here rather than after the handler returns.
		if ferr := http.NewResponseController(w).Flush(); ferr != nil && !errors.Is(ferr, http.ErrNotSupported) {
			err = ferr
		}
	}
	if err == nil || len(res.Messages) == 0 {
		return
	}
	leases := make([]string, len(res.Messages))
	for i, msg := range res.Messages {
		leases[i] = msg.LeaseID
	}
	m.releaseUndelivered(tenantID, name, leases)
}
