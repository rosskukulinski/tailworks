package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// Helpers for the /v1 API (api/openapi.yaml): paged lists and the host
// join flow.

// page is the /v1 list envelope.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// listAll fetches a /v1 list, following nextCursor. max > 0 stops after
// that many items; 0 fetches everything.
func listAll[T any](ctx context.Context, c *mgmtapi.Client, path string, max int) ([]T, error) {
	out, _, err := listUpTo[T](ctx, c, path, max)
	return out, err
}

// listUpTo is listAll that also says whether it stopped at max with items
// left over (tw ls's "truncated").
func listUpTo[T any](ctx context.Context, c *mgmtapi.Client, path string, max int) ([]T, bool, error) {
	out := []T{} // never nil: --json prints an empty list as [], not null
	cursor := ""
	for {
		u := path
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		limit := 500
		if max > 0 && max-len(out) < limit {
			limit = max - len(out)
		}
		u += sep + "limit=" + strconv.Itoa(limit)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var pg page[T]
		if err := c.Do(ctx, "GET", u, nil, &pg); err != nil {
			return out, false, err
		}
		out = append(out, pg.Items...)
		more := pg.NextCursor != nil && *pg.NextCursor != ""
		if !more || (max > 0 && len(out) >= max) {
			return out, more, nil
		}
		cursor = *pg.NextCursor
	}
}

// hostJoin is POST /v1/hosts (the key is never printed by the CLI).
type hostJoin struct {
	Host struct {
		HostID string `json:"hostID"`
		Status string `json:"status"`
	} `json:"host"`
	AuthKey string `json:"authKey"`
	// RegistrationSecret is the one-time secret hostd presents on its first
	// PUT /v1/hosts/{id}; it travels in the join file next to the key.
	RegistrationSecret string   `json:"registrationSecret"`
	ExpiresAt          int64    `json:"expiresAt"`
	Tags               []string `json:"tags"`
	Hostname           string   `json:"hostname"`
	ControlURL         string   `json:"controlURL"`
	Tailnet            string   `json:"tailnet"`

	// host_joinfile.go: what the join file carries besides the secrets.
	KeyTagsOnly bool   `json:"keyTagsOnly"`
	MgmtURL     string `json:"mgmtURL"`
}

func (hostJoin) String() string { return "hostJoin{[REDACTED]}" }

// joinFileContent is the join file tw-hostd reads: the single-use join
// key and the one-time registration secret (both 0600, never logged).
func (hj hostJoin) joinFileContent() []byte {
	return []byte("TS_AUTHKEY=" + hj.AuthKey + "\nTW_REGISTRATION_SECRET=" + hj.RegistrationSecret + "\n")
}

// mintHostJoin asks mgmt for this host's single-use join key and writes it
// to the join file tw-hostd waits for (0600 in hostd's 0700 state dir;
// hostd deletes it after the join).
func (s *state) mintHostJoin(ctx context.Context, hostID, tenantID string) error {
	var hj hostJoin
	if err := s.mgmt().DoHeaders(ctx, "POST", "/v1/hosts", map[string]any{"hostID": hostID, "tenantID": tenantID}, &hj,
		map[string]string{"Tw-Actor": actor()}); err != nil {
		if me, ok := mgmtapi.AsError(err); ok && me.Code == "exists" {
			return fail("state_conflict", "mgmt already has a host "+hostID+" bound to another node", me.Message).
				withFix("tw host rm "+hostID+" --force   (then tw up again)", "or tw up --host-id <another id>")
		}
		if me, ok := mgmtapi.AsError(err); ok && me.Code == "policy_missing_tag_owners" {
			return policyMissingTagOwners("mgmt could not mint the host's "+mgmt.HostTag+" join key: "+me.Message, nil, nil)
		}
		return err
	}
	if hj.AuthKey == "" || hj.RegistrationSecret == "" {
		return fail("bootstrap_failed", "mgmt returned no join key or registration secret for host "+hostID)
	}
	if err := os.MkdirAll(filepath.Dir(s.hostJoinFile()), 0o700); err != nil {
		return err
	}
	tmp := s.hostJoinFile() + ".tmp"
	if err := os.WriteFile(tmp, hj.hostJoinFileContent(hostID, s.hostJoinMgmtURL(hj)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.hostJoinFile())
}
