package mgmtapi

import (
	"context"

	v1 "github.com/rosskukulinski/tailworks/hostd/mgmtapi/v1"
)

// Queues (ADR-039): a workload's host sends for its producer bindings
// and claims and settles for its consumers, as the lease holder at epoch.

// QueueMessageIn is one message to send.
type QueueMessageIn struct {
	Body         []byte `json:"body"`
	ContentType  string `json:"contentType,omitempty"`
	DelaySeconds *int   `json:"delaySeconds,omitempty"`
}

// QueueSendResult is a send's answer (the queue's counters after it).
type QueueSendResult struct {
	IDs      []string `json:"ids"`
	Backlog  int64    `json:"backlog"`
	Bytes    int64    `json:"bytes"`
	OldestAt int64    `json:"oldestAt,omitempty"`
}

// QueueInfo is a queue and its counters.
type QueueInfo struct {
	Name     string `json:"name"`
	Backlog  int64  `json:"backlog"`
	Bytes    int64  `json:"bytes"`
	InFlight int64  `json:"inFlight"`
	Delayed  int64  `json:"delayed"`
	OldestAt int64  `json:"oldestAt,omitempty"`
}

// QueueMessage is one claimed message.
type QueueMessage struct {
	ID          string `json:"id"`
	LeaseID     string `json:"leaseId"`
	Body        []byte `json:"body"`
	ContentType string `json:"contentType"`
	Attempts    int64  `json:"attempts"`
	Timestamp   int64  `json:"timestamp"`
}

// QueuePullOpts is a claim's parameters.
type QueuePullOpts struct {
	BatchSize           int   `json:"batchSize,omitempty"`
	VisibilityTimeoutMs int64 `json:"visibilityTimeoutMs,omitempty"`
	WaitMs              int64 `json:"waitMs,omitempty"`
}

// QueuePullResult is a claim's answer.
type QueuePullResult struct {
	Messages     []QueueMessage `json:"messages"`
	DeadLettered int            `json:"deadLettered,omitempty"`
}

// QueueRetry is one retry.
type QueueRetry struct {
	LeaseID      string `json:"leaseId"`
	DelaySeconds *int   `json:"delaySeconds,omitempty"`
}

// QueueAck is a settle call.
type QueueAck struct {
	Acks                []string     `json:"acks,omitempty"`
	Retries             []QueueRetry `json:"retries,omitempty"`
	Extend              []string     `json:"extend,omitempty"`
	Release             []string     `json:"release,omitempty"`
	VisibilityTimeoutMs int64        `json:"visibilityTimeoutMs,omitempty"`
}

// QueueAckResult is a settle call's answer.
type QueueAckResult struct {
	Acked        int `json:"acked"`
	Retried      int `json:"retried"`
	DeadLettered int `json:"deadLettered"`
	Extended     int `json:"extended"`
	Released     int `json:"released"`
	Stale        int `json:"stale"`
}

func queueAs(workload string, epoch int64) (*string, *int64) { return &workload, &epoch }

// SendQueue sends msgs to queue for workload's producer binding.
func (c *Client) SendQueue(ctx context.Context, tenantID, queue, workload string, epoch int64, msgs []QueueMessageIn) (QueueSendResult, error) {
	var out QueueSendResult
	body, err := jsonBody(map[string]any{"messages": msgs})
	if err != nil {
		return out, err
	}
	w, e := queueAs(workload, epoch)
	resp, err := c.V1().SendQueueMessagesWithBodyWithResponse(ctx, tenantID, queue, &v1.SendQueueMessagesParams{Workload: w, Epoch: e}, "application/json", body)
	if err != nil {
		return out, err
	}
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}

// QueueInfo reads queue's counters for workload (a producer's metrics()).
func (c *Client) QueueInfo(ctx context.Context, tenantID, queue, workload string, epoch int64) (QueueInfo, error) {
	var out QueueInfo
	w, e := queueAs(workload, epoch)
	resp, err := c.V1().GetQueueWithResponse(ctx, tenantID, queue, &v1.GetQueueParams{Workload: w, Epoch: e})
	if err != nil {
		return out, err
	}
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}

// PullQueue claims a batch of queue for workload's consumer.
func (c *Client) PullQueue(ctx context.Context, tenantID, queue, workload string, epoch int64, o QueuePullOpts) (QueuePullResult, error) {
	var out QueuePullResult
	body, err := jsonBody(o)
	if err != nil {
		return out, err
	}
	w, e := queueAs(workload, epoch)
	resp, err := c.V1().PullQueueMessagesWithBodyWithResponse(ctx, tenantID, queue, &v1.PullQueueMessagesParams{Workload: w, Epoch: e}, "application/json", body)
	if err != nil {
		return out, err
	}
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}

// AckQueue settles claimed messages of queue for workload's consumer.
func (c *Client) AckQueue(ctx context.Context, tenantID, queue, workload string, epoch int64, a QueueAck) (QueueAckResult, error) {
	var out QueueAckResult
	body, err := jsonBody(a)
	if err != nil {
		return out, err
	}
	w, e := queueAs(workload, epoch)
	resp, err := c.V1().AckQueueMessagesWithBodyWithResponse(ctx, tenantID, queue, &v1.AckQueueMessagesParams{Workload: w, Epoch: e}, "application/json", body)
	if err != nil {
		return out, err
	}
	err = result(resp.HTTPResponse, resp.Body, &out)
	return out, err
}
