package main

// Queues (ADR-039), operator side: `tw queues create|ls|info|rm|purge`,
// `tw queues messages` (peek, e.g. a dead-letter queue) and `tw queues
// send`, like `wrangler queues`. Queues belong to the tenant; the commands
// go to mgmt's /v1 API, on the host or from a `tw link`ed machine.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/queuecfg"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// queueInput is where `tw queues send` reads a body when none is given
// on the command line (tests replace it).
var queueInput = func() io.Reader { return os.Stdin }

type queuesResult struct {
	TenantID string               `json:"tenantID"`
	Queue    *mgmt.QueueInfo      `json:"queue,omitempty"`
	Queues   *[]mgmt.QueueInfo    `json:"queues,omitempty"`   // ls: always present
	Messages *[]mgmt.QueueMessage `json:"messages,omitempty"` // messages: always present
	Sent     []string             `json:"sent,omitempty"`
	Deleted  string               `json:"deleted,omitempty"`
	Purged   *int64               `json:"purged,omitempty"`
}

const queuesSubs = "create | ls | info | rm | purge | messages | send"

// queuesOpts are tw queues' flags. A verb registers only its own
// (queuesFlags); the others keep their zero value.
type queuesOpts struct {
	tenant *string

	// messages
	limit *int

	// send
	text  *bool
	delay *int

	// rm, purge
	yes *bool
}

// queuesFlags registers verb's flags ("send"; anyVerb: every verb's) on f.
func queuesFlags(f *flags, verb string) *queuesOpts {
	o := &queuesOpts{limit: new(int), text: new(bool), delay: new(int), yes: new(bool)}
	o.tenant = tenantFlag(f)
	if onVerb(verb, "messages") {
		o.limit = f.Int("limit", 20, "at most this many (1-100)")
	}
	if onVerb(verb, "send") {
		o.text = f.Bool("text", false, "the body is text (contentType text), not JSON")
		o.delay = f.Int("delay", 0, "delaySeconds (0-43200)")
	}
	if onVerb(verb, "rm", "purge") {
		o.yes = yesFlag(f)
	}
	return o
}

func cmdQueues(args []string) error {
	sub, args, _ := scanVerb(args, func(f *flags) { queuesFlags(f, anyVerb) }, func(string) int { return 1 })
	if sub == "peek" {
		sub = "messages"
	}
	if sub != "" && !onVerb(sub, "create", "ls", "info", "rm", "purge", "messages", "send") {
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q (%s)", sub, queuesSubs), "usage: tw "+usageOf("queues"))
	}
	f := newFlags("queues")
	o := queuesFlags(f, sub)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	switch {
	case sub == "":
		return needsSubcommand("queues")
	case sub == "ls" && len(pos) != 0:
		return fail("bad_request", "tw queues ls takes no arguments", "usage: tw "+usageOf("queues"))
	case sub == "send" && (len(pos) < 1 || len(pos) > 2):
		return fail("bad_request", "tw queues send needs the queue (and a body, else stdin)", "usage: tw "+usageOf("queues"))
	case sub != "ls" && sub != "send" && len(pos) != 1:
		return fail("bad_request", "tw queues "+sub+" needs the queue name", "usage: tw "+usageOf("queues"))
	}
	name := ""
	if len(pos) > 0 {
		name = pos[0]
		if !queuecfg.ValidName(name) {
			return fail("bad_request", fmt.Sprintf("bad queue name %q: lowercase letters, digits and dashes, at most 63", name))
		}
	}
	switch sub {
	case "rm":
		if err := confirmDestroy(f, *o.yes, "deleting queue "+name+" deletes it with its messages", name, "tw queues rm "+name); err != nil {
			return err
		}
	case "purge":
		if err := confirmDestroy(f, *o.yes, "purging queue "+name+" deletes every message in it", name, "tw queues purge "+name,
			"tw queues messages "+name+"   (first, to see what's in it)"); err != nil {
			return err
		}
	}
	var msg map[string]any
	if sub == "send" {
		var body []byte
		if len(pos) == 2 {
			body = []byte(pos[1])
		} else if body, err = io.ReadAll(io.LimitReader(queueInput(), queuecfg.MaxMessageBytes+1)); err != nil {
			return fail("bad_request", "can't read the message body: "+err.Error())
		}
		ct := "json"
		if *o.text {
			ct = "text"
		} else if !json.Valid(body) {
			return fail("bad_request", "the body is not JSON").withFix("tw queues send "+name+" '{\"k\":1}'", "tw queues send "+name+" --text 'plain text'")
		}
		msg = map[string]any{"body": body, "contentType": ct}
		if *o.delay > 0 {
			msg["delaySeconds"] = *o.delay
		}
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*o.tenant)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/queues"
	hdr := map[string]string{"Tw-Actor": actor()}
	res := queuesResult{TenantID: tn.TenantID}
	c := s.mgmt()
	switch sub {
	case "ls":
		items, err := listAll[mgmt.QueueInfo](ctx, c, base, 0)
		if err != nil {
			return err
		}
		qs := append([]mgmt.QueueInfo{}, items...)
		res.Queues = &qs
		emit(f, res, func() {
			if len(items) == 0 {
				fmt.Printf("no queues in tenant %s · a producer's first send creates one, or: tw queues create <name>\n", tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %8s %9s %8s %10s %s\n", "QUEUE", "BACKLOG", "IN FLIGHT", "DELAYED", "BYTES", "CONSUMER")
			for _, q := range items {
				fmt.Printf("%-32s %8d %9d %8d %10d %s\n", tsafe(q.Name), q.Backlog, q.InFlight, q.Delayed, q.Bytes, tsafe(orDash(q.Consumer)))
			}
		})
	case "create", "info":
		method := "GET"
		if sub == "create" {
			method = "PUT"
		}
		var q mgmt.QueueInfo
		if err := c.DoHeaders(ctx, method, base+"/"+name, nil, &q, hdr); err != nil {
			return err
		}
		res.Queue = &q
		emit(f, res, func() {
			if sub == "create" {
				fmt.Printf("✓ queue %s (tenant %s)\n", name, tn.Tailnet)
				return
			}
			fmt.Printf("queue %s (tenant %s)\n", name, tn.Tailnet)
			fmt.Printf("  backlog    %d messages, %d bytes (%d in flight, %d delayed)\n", q.Backlog, q.Bytes, q.InFlight, q.Delayed)
			if q.OldestAt > 0 {
				fmt.Printf("  oldest     %s\n", time.UnixMilli(q.OldestAt).Local().Format("2006-01-02 15:04:05"))
			}
			if q.Consumer != "" {
				fmt.Printf("  consumer   %s (last claim %s)\n", tsafe(q.Consumer), time.UnixMilli(q.ConsumedAt).Local().Format("2006-01-02 15:04:05"))
			}
			fmt.Printf("  created    %s\n", time.UnixMilli(q.CreatedAt).Local().Format("2006-01-02 15:04:05"))
		})
	case "rm":
		if err := c.DoHeaders(ctx, "DELETE", base+"/"+name, nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = name
		emit(f, res, func() { fmt.Printf("✓ queue %s deleted with its messages (tenant %s)\n", name, tn.Tailnet) })
	case "purge":
		var pr struct{ Purged int64 }
		if err := c.DoHeaders(ctx, "POST", base+"/"+name+"/purge", nil, &pr, hdr); err != nil {
			return err
		}
		res.Purged = &pr.Purged
		emit(f, res, func() { fmt.Printf("✓ purged %d messages from queue %s\n", pr.Purged, name) })
	case "messages":
		var out struct{ Messages []mgmt.QueueMessage }
		if err := c.DoHeaders(ctx, "GET", base+"/"+name+"/messages?limit="+strconv.Itoa(*o.limit), nil, &out, hdr); err != nil {
			return err
		}
		msgs := append([]mgmt.QueueMessage{}, out.Messages...)
		res.Messages = &msgs
		emit(f, res, func() {
			if len(out.Messages) == 0 {
				fmt.Printf("queue %s is empty\n", name)
				return
			}
			for _, m := range out.Messages {
				body := string(m.Body)
				if m.ContentType == "bytes" || m.ContentType == "v8" {
					body = fmt.Sprintf("<%d bytes>", len(m.Body))
				}
				if len(body) > 200 {
					body = body[:200] + "…"
				}
				fmt.Printf("%s  %s  attempts %d  %-5s %s\n", tsafe(m.ID), time.UnixMilli(m.Timestamp).Local().Format("2006-01-02 15:04:05"), m.Attempts, tsafe(m.ContentType), tsafe(body))
			}
		})
	case "send":
		var sr mgmt.QueueSendResult
		if err := c.DoHeaders(ctx, "POST", base+"/"+name+"/messages", map[string]any{"messages": []any{msg}}, &sr, hdr); err != nil {
			return err
		}
		res.Sent = sr.IDs
		emit(f, res, func() {
			fmt.Printf("✓ sent %s to queue %s (backlog %d)\n", strings.Join(sr.IDs, ", "), name, sr.Backlog)
		})
	default:
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q (%s)", sub, queuesSubs), "usage: tw "+usageOf("queues"))
	}
	return nil
}
