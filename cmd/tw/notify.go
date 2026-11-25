package main

// `tw notify show|set|test|rm` (ADR-047): the tenant's
// notification webhook. When a Workflow starts waiting for an approval,
// mgmt POSTs a JSON event with a link to the admin UI's approvals row (and
// a one-line "text" that Slack, Mattermost and Google Chat incoming
// webhooks show as is). The URL is a secret (a chat webhook's path is its
// credential): give it as --webhook - to read it from stdin; it is never
// shown again, only its origin.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// notifyView is GET/PUT /v1/tenants/{id}/notifications, and `tw notify
// test`'s outcome.
type notifyView struct {
	TenantID string `json:"tenantID"`
	Webhook  *struct {
		Origin      string   `json:"origin"`
		HeaderNames []string `json:"headerNames"`
		Events      []string `json:"events"`
	} `json:"webhook,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	// test
	Delivered *bool  `json:"delivered,omitempty"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// notifyOpts are tw notify's flags. Only set has its own (notifyFlags).
type notifyOpts struct {
	tenant *string

	// set
	webhook *string
	headers *repeated
}

// notifyFlags registers verb's flags (anyVerb: every verb's) on f.
func notifyFlags(f *flags, verb string) *notifyOpts {
	o := &notifyOpts{webhook: new(string), headers: &repeated{}}
	o.tenant = tenantFlag(f)
	if onVerb(verb, "set") {
		o.webhook = f.String("webhook", "", "the URL mgmt POSTs to (https://hooks.slack.com/services/…, http://chat-bot/notify); - reads it from stdin")
		f.Var(o.headers, "header", "a header for every notification, NAME=VALUE, or NAME alone to read its value from stdin (repeatable)")
	}
	return o
}

func cmdNotify(args []string) error {
	sub, rest := scanVerbOr(args, func(f *flags) { notifyFlags(f, anyVerb) }, "show")
	f := newFlags("notify")
	o := notifyFlags(f, sub)
	tenantF, hook := o.tenant, o.webhook
	pos, err := f.parse(rest)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fail("bad_request", "unexpected arguments", "usage: tw "+usageOf("notify"))
	}
	headers := *o.headers
	put := map[string]any{}
	switch sub {
	case "show", "ls":
		sub = "show"
	case "rm", "off":
		sub = "rm"
	case "test":
	case "set":
		u := *hook
		if u == "-" {
			if u, err = readSecretValue("webhook URL"); err != nil {
				return err
			}
			u = strings.TrimSpace(u)
		}
		if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return fail("bad_request", "--webhook must be an http:// or https:// URL").withFix("tw notify set --webhook -   (paste the URL; it is a secret)")
		}
		put["url"] = u
		hs := map[string]string{}
		for _, h := range headers {
			name, value, ok := strings.Cut(h, "=")
			if !ok {
				if value, err = readHeaderValue(name); err != nil {
					return err
				}
			}
			hs[strings.TrimSpace(name)] = value
		}
		if len(hs) > 0 {
			put["headers"] = hs
		}
	default:
		return fail("bad_request", fmt.Sprintf("unknown subcommand %q (show, set, test or rm)", sub), "usage: tw "+usageOf("notify"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/notifications"
	hdr := map[string]string{"Tw-Actor": actor()}
	var v notifyView
	switch sub {
	case "show":
		err = s.mgmt().Do(ctx, "GET", path, nil, &v)
	case "set":
		err = s.mgmt().DoHeaders(ctx, "PUT", path, map[string]any{"webhook": put}, &v, hdr)
	case "test":
		var res struct {
			Delivered bool   `json:"delivered"`
			Status    int    `json:"status"`
			Error     string `json:"error"`
		}
		err = s.mgmt().DoHeaders(ctx, "POST", path+"/test", nil, &res, hdr)
		v = notifyView{TenantID: tn.TenantID, Delivered: &res.Delivered, Status: res.Status, Error: res.Error}
	case "rm":
		err = s.mgmt().DoHeaders(ctx, "DELETE", path, nil, nil, hdr)
		v.TenantID = tn.TenantID
	}
	if err != nil {
		return err
	}
	emit(f, v, func() {
		switch {
		case sub == "rm":
			fmt.Printf("✓ notifications off for tenant %s\n", tn.Tailnet)
		case sub == "test" && *v.Delivered:
			fmt.Printf("✓ test notification delivered (HTTP %d)\n", v.Status)
		case sub == "test":
			fmt.Printf("✗ test notification not delivered: %s\n", tsafe(v.Error))
		case v.Webhook == nil:
			fmt.Printf("tenant %s sends no notifications: approvals wait until someone looks (tw approvals, the admin UI)\n", tn.Tailnet)
			fmt.Printf("  notify: tw notify set --webhook -   (paste a Slack/chat incoming-webhook URL, or a tailnet URL)\n")
		default:
			if sub == "set" {
				fmt.Printf("✓ notifications set for tenant %s; try: tw notify test\n", tn.Tailnet)
			}
			fmt.Printf("  webhook   %s/… (the rest of the URL is never shown)\n  events    %s\n", v.Webhook.Origin, strings.Join(v.Webhook.Events, ", "))
			if len(v.Webhook.HeaderNames) > 0 {
				fmt.Printf("  headers   %s (values are never shown)\n", strings.Join(v.Webhook.HeaderNames, ", "))
			}
		}
	})
	return nil
}
