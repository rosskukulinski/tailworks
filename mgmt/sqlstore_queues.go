package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Queues (ADR-039): one row per tenant queue, and its messages. Messages
// are keyed by the queue's incarnation (random per creation), so deleting a
// queue and creating it again never mixes their messages. Bodies are
// sealed by the caller (queues.go); this file never sees plaintext.
const schemaQueues = `
CREATE TABLE IF NOT EXISTS queues (
  tenant_id TEXT NOT NULL, name TEXT NOT NULL, incarnation TEXT NOT NULL,
  backlog BIGINT NOT NULL, bytes BIGINT NOT NULL, seq BIGINT NOT NULL,
  consumer TEXT NOT NULL DEFAULT '', consumed_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
  PRIMARY KEY (tenant_id, name));
CREATE TABLE IF NOT EXISTS queue_messages (
  incarnation TEXT NOT NULL, id TEXT NOT NULL, seq BIGINT NOT NULL,
  content_type TEXT NOT NULL, size BIGINT NOT NULL, body %s NOT NULL,
  attempts BIGINT NOT NULL, sent_at BIGINT NOT NULL, visible_at BIGINT NOT NULL,
  lease TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (incarnation, id));
CREATE INDEX IF NOT EXISTS queue_messages_visible ON queue_messages (incarnation, visible_at, seq);
`

// errQueueGone: the queue was deleted (or deleted and created again)
// between the caller's lookup and its write.
var errQueueGone = errors.New("queue_gone")

// errQueueFull: the send would take the queue over its byte limit.
var errQueueFull = errors.New("queue_limit")

// queueMsgRow is one stored message (body sealed).
type queueMsgRow struct {
	ID          string
	Seq         int64
	ContentType string
	Size        int64
	Body        []byte
	Attempts    int64
	SentAt      int64
	VisibleAt   int64
	Lease       string
}

// queueTarget is where dead-lettered messages go: a queue (already
// created) and how to re-seal a body for it (open with the source queue's
// key, seal with this one's).
type queueTarget struct {
	TenantID, Name, Incarnation string
	Reseal                      func(id string, sealed []byte) ([]byte, error)
}

// queueClaim is one claim's parameters.
type queueClaim struct {
	Max          int
	Now          int64
	VisibilityMs int64
	MaxRetries   int64
	DLQ          *queueTarget // nil: expired messages past MaxRetries are dropped
	Consumer     string
	NewLease     func() string
}

// queueRetry is one retry: a lease and its delay (ms).
type queueRetry struct {
	Lease   string
	DelayMs int64
}

// queueSettle is one settle call's parameters.
type queueSettle struct {
	Acks         []string // "<id>:<token>"
	Retries      []queueRetry
	Extend       []string
	Release      []string
	Now          int64
	VisibilityMs int64
	MaxRetries   int64
	DLQ          *queueTarget
}

// QueueAckResult is POST …/messages/ack's answer.
type QueueAckResult struct {
	Acked        int `json:"acked"`
	Retried      int `json:"retried"`
	DeadLettered int `json:"deadLettered"`
	Extended     int `json:"extended"`
	Released     int `json:"released"`
	Stale        int `json:"stale"`
}

// queueStore is implemented by stores that can hold queues (SQLStore).
type queueStore interface {
	CreateQueue(ctx context.Context, q QueueInfo) (created bool, err error)
	GetQueue(ctx context.Context, tenantID, name string) (QueueInfo, error)
	ListQueues(ctx context.Context, tenantID string, pg Page) ([]QueueInfo, error)
	// QueueStats fills the computed counters of q (in flight, delayed,
	// oldest) at now.
	QueueStats(ctx context.Context, q *QueueInfo, now int64) error
	DeleteQueue(ctx context.Context, tenantID, name string) (QueueInfo, error)
	PurgeQueue(ctx context.Context, tenantID, name string, now int64) (int64, error)
	// SendQueue inserts msgs (sealed, with ID, size, times) into the queue
	// of incarnation inc, giving them the next seqs. errQueueGone if the
	// queue's incarnation is no longer inc, errQueueFull past maxBytes, a
	// *quotaError past the tenant's tenantMax for all its queues (0 = no
	// tenant quota; quota.go).
	SendQueue(ctx context.Context, tenantID, name, inc string, msgs []queueMsgRow, maxBytes, tenantMax, now int64) (QueueInfo, error)
	// ClaimQueue leases up to c.Max visible messages (oldest first).
	// Visible messages whose attempts already exceed c.MaxRetries are
	// dead-lettered instead (counted in the second result).
	ClaimQueue(ctx context.Context, tenantID, name, inc string, c queueClaim) ([]queueMsgRow, int, error)
	SettleQueue(ctx context.Context, tenantID, name, inc string, st queueSettle) (QueueAckResult, error)
	// PeekQueue returns up to limit messages, oldest first, unchanged.
	PeekQueue(ctx context.Context, inc string, limit int) ([]queueMsgRow, error)
}

const queueCols = "tenant_id, name, incarnation, backlog, bytes, seq, consumer, consumed_at, created_at, updated_at"

func scanQueue(sc interface{ Scan(...any) error }) (QueueInfo, error) {
	var q QueueInfo
	err := sc.Scan(&q.TenantID, &q.Name, &q.Incarnation, &q.Backlog, &q.Bytes, &q.seq, &q.Consumer, &q.ConsumedAt, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

const queueMsgCols = "id, seq, content_type, size, body, attempts, sent_at, visible_at, lease"

func scanQueueMsg(sc interface{ Scan(...any) error }) (queueMsgRow, error) {
	var m queueMsgRow
	err := sc.Scan(&m.ID, &m.Seq, &m.ContentType, &m.Size, &m.Body, &m.Attempts, &m.SentAt, &m.VisibleAt, &m.Lease)
	return m, err
}

// skipLocked lets concurrent claims on Postgres pass rows another claim
// holds (SQLite serializes writers anyway).
func (s *SQLStore) skipLocked() string {
	if s.d == dialectPostgres {
		return " FOR UPDATE SKIP LOCKED"
	}
	return ""
}

func (s *SQLStore) CreateQueue(ctx context.Context, q QueueInfo) (bool, error) {
	res, err := s.db.ExecContext(ctx, s.q("INSERT INTO queues ("+queueCols+") VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING"),
		q.TenantID, q.Name, q.Incarnation, 0, 0, 0, "", 0, q.CreatedAt, q.UpdatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQLStore) GetQueue(ctx context.Context, tenantID, name string) (QueueInfo, error) {
	return scanQueue(s.db.QueryRowContext(ctx, s.q("SELECT "+queueCols+" FROM queues WHERE tenant_id=? AND name=?"), tenantID, name))
}

func (s *SQLStore) ListQueues(ctx context.Context, tenantID string, pg Page) ([]QueueInfo, error) {
	q := "SELECT " + queueCols + " FROM queues WHERE tenant_id=?"
	args := []any{tenantID}
	if pg.After != nil {
		q += " AND name > ?"
		args = append(args, pg.After.ID)
	}
	q += " ORDER BY name"
	if pg.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, pg.Limit)
	}
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueInfo
	for rows.Next() {
		qi, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, qi)
	}
	return out, rows.Err()
}

func (s *SQLStore) QueueStats(ctx context.Context, q *QueueInfo, now int64) error {
	var oldest sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT
		COUNT(CASE WHEN visible_at > ? AND lease <> '' THEN 1 END),
		COUNT(CASE WHEN visible_at > ? AND lease = '' THEN 1 END),
		MIN(sent_at) FROM queue_messages WHERE incarnation=?`), now, now, q.Incarnation).Scan(&q.InFlight, &q.Delayed, &oldest)
	if err != nil {
		return err
	}
	if oldest.Valid {
		q.OldestAt = oldest.Int64
	}
	return nil
}

func (s *SQLStore) DeleteQueue(ctx context.Context, tenantID, name string) (QueueInfo, error) {
	var q QueueInfo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		q, err = scanQueue(tx.QueryRowContext(ctx, s.q("SELECT "+queueCols+" FROM queues WHERE tenant_id=? AND name=?"+s.forUpdate()), tenantID, name))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM queue_messages WHERE incarnation=?"), q.Incarnation); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.q("DELETE FROM queues WHERE tenant_id=? AND name=?"), tenantID, name)
		return err
	})
	return q, err
}

func (s *SQLStore) PurgeQueue(ctx context.Context, tenantID, name string, now int64) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		q, err := scanQueue(tx.QueryRowContext(ctx, s.q("SELECT "+queueCols+" FROM queues WHERE tenant_id=? AND name=?"+s.forUpdate()), tenantID, name))
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, s.q("DELETE FROM queue_messages WHERE incarnation=?"), q.Incarnation)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, s.q("UPDATE queues SET backlog=0, bytes=0, updated_at=? WHERE tenant_id=? AND name=?"), now, tenantID, name)
		return err
	})
	return n, err
}

// insertQueueMsgs inserts msgs into (tenant, name) of incarnation inc in
// tx, after checking the incarnation (and locking the row on Postgres).
func (s *SQLStore) insertQueueMsgs(ctx context.Context, tx *sql.Tx, tenantID, name, inc string, msgs []queueMsgRow, maxBytes, now int64) (QueueInfo, error) {
	q, err := scanQueue(tx.QueryRowContext(ctx, s.q("SELECT "+queueCols+" FROM queues WHERE tenant_id=? AND name=?"+s.forUpdate()), tenantID, name))
	if errors.Is(err, ErrNotFound) {
		return q, errQueueGone
	}
	if err != nil {
		return q, err
	}
	if q.Incarnation != inc {
		return q, errQueueGone
	}
	for _, m := range msgs {
		q.seq++
		if _, err := tx.ExecContext(ctx, s.q("INSERT INTO queue_messages ("+queueMsgCols+", incarnation) VALUES (?,?,?,?,?,?,?,?,?,?)"),
			m.ID, q.seq, m.ContentType, m.Size, m.Body, m.Attempts, m.SentAt, m.VisibleAt, "", inc); err != nil {
			return q, err
		}
		q.Backlog++
		q.Bytes += m.Size
	}
	if maxBytes > 0 && q.Bytes > maxBytes {
		return q, errQueueFull
	}
	q.UpdatedAt = now
	_, err = tx.ExecContext(ctx, s.q("UPDATE queues SET backlog=?, bytes=?, seq=?, updated_at=? WHERE tenant_id=? AND name=?"),
		q.Backlog, q.Bytes, q.seq, now, tenantID, name)
	return q, err
}

func (s *SQLStore) SendQueue(ctx context.Context, tenantID, name, inc string, msgs []queueMsgRow, maxBytes, tenantMax, now int64) (QueueInfo, error) {
	var q QueueInfo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.checkTenantQueueBytes(ctx, tx, tenantID, msgs, tenantMax); err != nil { // sqlstore_quota.go
			return err
		}
		var err error
		q, err = s.insertQueueMsgs(ctx, tx, tenantID, name, inc, msgs, maxBytes, now)
		return err
	})
	return q, err
}

// dropQueueCounters takes n messages of bytes off the queue's counters.
func (s *SQLStore) dropQueueCounters(ctx context.Context, tx *sql.Tx, tenantID, name string, n, bytes, now int64) error {
	if n == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.q("UPDATE queues SET backlog=backlog-?, bytes=bytes-?, updated_at=? WHERE tenant_id=? AND name=?"),
		n, bytes, now, tenantID, name)
	return err
}

// deadLetter moves (or drops, dlq nil) the rows from incarnation inc in tx.
func (s *SQLStore) deadLetter(ctx context.Context, tx *sql.Tx, tenantID, name, inc string, rows []queueMsgRow, dlq *queueTarget, now int64) error {
	if len(rows) == 0 {
		return nil
	}
	var bytes int64
	for _, m := range rows {
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM queue_messages WHERE incarnation=? AND id=?"), inc, m.ID); err != nil {
			return err
		}
		bytes += m.Size
	}
	if err := s.dropQueueCounters(ctx, tx, tenantID, name, int64(len(rows)), bytes, now); err != nil {
		return err
	}
	if dlq == nil {
		return nil
	}
	moved := make([]queueMsgRow, len(rows))
	for i, m := range rows {
		b, err := dlq.Reseal(m.ID, m.Body)
		if err != nil {
			return err
		}
		moved[i] = queueMsgRow{ID: m.ID, ContentType: m.ContentType, Size: m.Size, Body: b, SentAt: m.SentAt, VisibleAt: now}
	}
	_, err := s.insertQueueMsgs(ctx, tx, dlq.TenantID, dlq.Name, dlq.Incarnation, moved, 0, now)
	return err
}

func (s *SQLStore) ClaimQueue(ctx context.Context, tenantID, name, inc string, c queueClaim) ([]queueMsgRow, int, error) {
	var out []queueMsgRow
	dead := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		out, dead = nil, 0
		rows, err := tx.QueryContext(ctx, s.q("SELECT "+queueMsgCols+" FROM queue_messages WHERE incarnation=? AND visible_at<=? ORDER BY visible_at, seq LIMIT ?"+s.skipLocked()),
			inc, c.Now, c.Max)
		if err != nil {
			return err
		}
		var got []queueMsgRow
		for rows.Next() {
			m, err := scanQueueMsg(rows)
			if err != nil {
				rows.Close()
				return err
			}
			got = append(got, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var expired []queueMsgRow
		for _, m := range got {
			if m.Attempts > c.MaxRetries {
				// Its last delivery's lease expired without a settle (the
				// consumer crashed) and it had no retries left.
				expired = append(expired, m)
				continue
			}
			m.Attempts++
			m.VisibleAt = c.Now + c.VisibilityMs
			m.Lease = c.NewLease()
			if _, err := tx.ExecContext(ctx, s.q("UPDATE queue_messages SET attempts=?, visible_at=?, lease=? WHERE incarnation=? AND id=?"),
				m.Attempts, m.VisibleAt, m.Lease, inc, m.ID); err != nil {
				return err
			}
			out = append(out, m)
		}
		if err := s.deadLetter(ctx, tx, tenantID, name, inc, expired, c.DLQ, c.Now); err != nil {
			return err
		}
		dead = len(expired)
		if len(out) > 0 && c.Consumer != "" {
			if _, err := tx.ExecContext(ctx, s.q("UPDATE queues SET consumer=?, consumed_at=? WHERE tenant_id=? AND name=?"), c.Consumer, c.Now, tenantID, name); err != nil {
				return err
			}
		}
		return nil
	})
	return out, dead, err
}

// splitLease splits "<id>:<token>".
func splitLease(l string) (id, token string, ok bool) {
	id, token, ok = strings.Cut(l, ":")
	return id, token, ok && id != "" && token != ""
}

func (s *SQLStore) SettleQueue(ctx context.Context, tenantID, name, inc string, st queueSettle) (QueueAckResult, error) {
	var res QueueAckResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res = QueueAckResult{}
		// current returns the message a lease names, if the lease is current.
		current := func(lease string) (queueMsgRow, bool, error) {
			id, token, ok := splitLease(lease)
			if !ok {
				return queueMsgRow{}, false, nil
			}
			m, err := scanQueueMsg(tx.QueryRowContext(ctx, s.q("SELECT "+queueMsgCols+" FROM queue_messages WHERE incarnation=? AND id=? AND lease=?"+s.forUpdate()), inc, id, token))
			if errors.Is(err, sql.ErrNoRows) {
				return m, false, nil
			}
			return m, err == nil, err
		}
		var ackN, ackBytes int64
		for _, l := range st.Acks {
			m, ok, err := current(l)
			if err != nil {
				return err
			}
			if !ok {
				res.Stale++
				continue
			}
			if _, err := tx.ExecContext(ctx, s.q("DELETE FROM queue_messages WHERE incarnation=? AND id=?"), inc, m.ID); err != nil {
				return err
			}
			ackN++
			ackBytes += m.Size
			res.Acked++
		}
		if err := s.dropQueueCounters(ctx, tx, tenantID, name, ackN, ackBytes, st.Now); err != nil {
			return err
		}
		var dead []queueMsgRow
		for _, r := range st.Retries {
			m, ok, err := current(r.Lease)
			if err != nil {
				return err
			}
			if !ok {
				res.Stale++
				continue
			}
			if m.Attempts > st.MaxRetries {
				dead = append(dead, m)
				continue
			}
			if _, err := tx.ExecContext(ctx, s.q("UPDATE queue_messages SET visible_at=?, lease='' WHERE incarnation=? AND id=?"), st.Now+r.DelayMs, inc, m.ID); err != nil {
				return err
			}
			res.Retried++
		}
		if err := s.deadLetter(ctx, tx, tenantID, name, inc, dead, st.DLQ, st.Now); err != nil {
			return err
		}
		res.DeadLettered = len(dead)
		for _, l := range st.Extend {
			m, ok, err := current(l)
			if err != nil {
				return err
			}
			if !ok {
				res.Stale++
				continue
			}
			if _, err := tx.ExecContext(ctx, s.q("UPDATE queue_messages SET visible_at=? WHERE incarnation=? AND id=?"), st.Now+st.VisibilityMs, inc, m.ID); err != nil {
				return err
			}
			res.Extended++
		}
		for _, l := range st.Release {
			m, ok, err := current(l)
			if err != nil {
				return err
			}
			if !ok {
				res.Stale++
				continue
			}
			if _, err := tx.ExecContext(ctx, s.q("UPDATE queue_messages SET visible_at=?, lease='', attempts=? WHERE incarnation=? AND id=?"), st.Now, max(m.Attempts-1, 0), inc, m.ID); err != nil {
				return err
			}
			res.Released++
		}
		return nil
	})
	return res, err
}

func (s *SQLStore) PeekQueue(ctx context.Context, inc string, limit int) ([]queueMsgRow, error) {
	rows, err := s.db.QueryContext(ctx, s.q("SELECT "+queueMsgCols+" FROM queue_messages WHERE incarnation=? ORDER BY seq LIMIT ?"), inc, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queueMsgRow
	for rows.Next() {
		m, err := scanQueueMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
