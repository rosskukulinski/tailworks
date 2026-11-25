// Package headscale is the Headscale profile's control-plane API client
// (ADR-015, RFC §7.2): the subset of Headscale's REST API (/api/v1, the
// gRPC gateway on the server's own listener) that tailworks needs, shaped like
// the Tailscale API client in internal/tsapi so mgmt and the CLI can use
// either.
//
// Mapping (Headscale v0.29):
//   - CreateKey → POST /api/v1/preauthkey: a tagged pre-auth key with no
//     user (the node is owned by its tags). Headscale keys are always
//     pre-authorized; descriptions don't exist.
//   - DeleteKey → POST /api/v1/preauthkey/expire.
//   - DeleteDevice → DELETE /api/v1/node/{id}. A Headscale node's
//     StableNodeID is its decimal node ID, so the IDs tailworks records from the
//     netmap work unchanged.
//   - ListDevices → GET /api/v1/node.
//   - policy → GET/PUT /api/v1/policy, POST /api/v1/policy/check. The ETag
//     is a hash of the policy text: Headscale has no conditional write, so
//     SetPolicy re-reads and compares just before it writes (best effort;
//     a concurrent writer between the two calls is not detected).
//
// The API key (headscale apikeys create) is a long-lived secret: Headscale
// has no workload identity federation. Keys and API keys are never logged
// or included in errors.
package headscale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/tailscale/hujson"
)

// APIKeyEnv is the variable holding the Headscale API key in the 0600
// credential file (as TS_API_KEY does for the Tailscale API).
const APIKeyEnv = "HEADSCALE_API_KEY"

// DefaultKeyExpiry is used when KeyOptions.ExpirySeconds is unset: a
// Headscale key with no expiry would never expire.
const DefaultKeyExpiry = time.Hour

// Client talks to one Headscale server.
type Client struct {
	BaseURL string // the server_url, e.g. https://hs.example.com (required)
	APIKey  string // required
	// BaseDomain is the server's dns.base_domain (tailworks' "tailnet" for a
	// Headscale tenant); it completes device names in ListDevices.
	BaseDomain string
	HTTP       *http.Client
	Now        func() time.Time // tests
}

func (c *Client) String() string { return "headscale.Client{" + c.BaseURL + "}" }

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// CreateKey mints a tagged, single-use (unless Reusable) pre-auth key.
func (c *Client) CreateKey(ctx context.Context, o tsapi.KeyOptions) (tsapi.Key, error) {
	exp := DefaultKeyExpiry
	if o.ExpirySeconds > 0 {
		exp = time.Duration(o.ExpirySeconds) * time.Second
	}
	body := map[string]any{
		"reusable":   o.Reusable,
		"ephemeral":  o.Ephemeral,
		"expiration": c.now().Add(exp).UTC().Format(time.RFC3339),
	}
	if len(o.Tags) > 0 {
		body["aclTags"] = o.Tags
	}
	var out struct {
		PreAuthKey *struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"preAuthKey"`
	}
	if err := c.do(ctx, "POST", "/api/v1/preauthkey", body, &out); err != nil {
		return tsapi.Key{}, err
	}
	if out.PreAuthKey == nil || out.PreAuthKey.ID == "" || out.PreAuthKey.Key == "" {
		return tsapi.Key{}, errors.New("headscale: pre-auth key response missing id or key")
	}
	return tsapi.Key{ID: out.PreAuthKey.ID, Secret: out.PreAuthKey.Key}, nil
}

// DeleteKey expires a pre-auth key. A key that no longer exists is not an
// error.
func (c *Client) DeleteKey(ctx context.Context, id string) error {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return fmt.Errorf("headscale: pre-auth key id %q is not numeric", id)
	}
	err = c.do(ctx, "POST", "/api/v1/preauthkey/expire", map[string]string{"id": strconv.FormatUint(n, 10)}, nil)
	if tsapi.IsNotFound(err) {
		return nil
	}
	return err
}

// DeleteDevice deletes a node by its ID (= its StableNodeID on Headscale).
// A node that doesn't exist is not an error.
func (c *Client) DeleteDevice(ctx context.Context, id string) error {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return fmt.Errorf("headscale: node id %q is not numeric", id)
	}
	err := c.do(ctx, "DELETE", "/api/v1/node/"+url.PathEscape(id), nil, nil)
	if tsapi.IsNotFound(err) {
		return nil
	}
	return err
}

// ListDevices lists the server's nodes as tsapi.Devices (ID and NodeID
// are both the node ID; Name is the MagicDNS name).
func (c *Client) ListDevices(ctx context.Context) ([]tsapi.Device, error) {
	var out struct {
		Nodes []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			GivenName string   `json:"givenName"`
			Tags      []string `json:"tags"`
		} `json:"nodes"`
	}
	if err := c.do(ctx, "GET", "/api/v1/node", nil, &out); err != nil {
		return nil, err
	}
	devs := make([]tsapi.Device, 0, len(out.Nodes))
	for _, n := range out.Nodes {
		name := n.GivenName
		if name == "" {
			name = n.Name
		}
		if c.BaseDomain != "" {
			name += "." + c.BaseDomain
		}
		devs = append(devs, tsapi.Device{ID: n.ID, NodeID: n.ID, Name: name, Hostname: n.Name, Tags: n.Tags})
	}
	return devs, nil
}

// GetPolicyHuJSON returns the policy as stored (HuJSON, comments kept) and
// its ETag. A server with no policy yet returns "{}" and an empty ETag.
func (c *Client) GetPolicyHuJSON(ctx context.Context) ([]byte, string, error) {
	var out struct {
		Policy string `json:"policy"`
	}
	err := c.do(ctx, "GET", "/api/v1/policy", nil, &out)
	if isNoPolicy(err) {
		return []byte("{}"), "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(out.Policy) == "" {
		return []byte("{}"), "", nil
	}
	return []byte(out.Policy), etagOf(out.Policy), nil
}

// GetPolicy returns the policy as standard JSON. With etag set it reports
// notModified when the policy text is unchanged (Headscale has no
// conditional read, so the policy is always fetched).
func (c *Client) GetPolicy(ctx context.Context, etag string) ([]byte, string, bool, error) {
	raw, tag, err := c.GetPolicyHuJSON(ctx)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" && tag == etag {
		return nil, tag, true, nil
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, "", false, fmt.Errorf("headscale: policy: %w", err)
	}
	return std, tag, false, nil
}

// MinVersion is the oldest Headscale the profile supports: grants (where
// tailworks' caps travel) and POST /api/v1/policy/check arrived in v0.29;
// v0.28's parser rejects "grants" outright.
const MinVersion = "v0.29"

// ErrTooOld means the server predates MinVersion.
var ErrTooOld = errors.New("headscale: the server is older than " + MinVersion + " (no grants, no /api/v1/policy/check); the Headscale profile needs " + MinVersion + " or later")

// ValidatePolicy asks the server to parse and check body without applying
// it (POST /api/v1/policy/check). A server without that endpoint is too
// old for the profile (ErrTooOld).
func (c *Client) ValidatePolicy(ctx context.Context, body []byte) error {
	err := c.do(ctx, "POST", "/api/v1/policy/check", map[string]string{"policy": string(body)}, nil)
	if tsapi.IsNotFound(err) {
		var e *tsapi.APIError
		if errors.As(err, &e) && e.Message == "Not Found" {
			return ErrTooOld
		}
	}
	return err
}

// CheckVersion reports ErrTooOld for a server older than MinVersion.
func (c *Client) CheckVersion(ctx context.Context) error {
	err := c.ValidatePolicy(ctx, []byte("{}"))
	if errors.Is(err, ErrTooOld) || tsapi.IsUnauthorized(err) {
		return err
	}
	return nil // a 400 for "{}" is still a server that has the endpoint
}

// SetPolicy writes body if the stored policy still has ETag ifMatch ("" =
// no policy yet). It fails with a 412 APIError when the policy changed.
// Headscale refuses writes unless policy.mode is "database".
func (c *Client) SetPolicy(ctx context.Context, body []byte, ifMatch string) (string, error) {
	_, cur, err := c.GetPolicyHuJSON(ctx)
	if err != nil {
		return "", err
	}
	if cur != ifMatch {
		return "", &tsapi.APIError{Status: http.StatusPreconditionFailed, Message: "the policy changed since it was read", API: "headscale api"}
	}
	var out struct {
		Policy string `json:"policy"`
	}
	if err := c.do(ctx, "PUT", "/api/v1/policy", map[string]string{"policy": string(body)}, &out); err != nil {
		return "", err
	}
	return etagOf(string(body)), nil
}

// Health reports whether the server answers /api/v1/health with a working
// database (it checks the API key too).
func (c *Client) Health(ctx context.Context) error {
	var out struct {
		DatabaseConnectivity bool `json:"databaseConnectivity"`
	}
	if err := c.do(ctx, "GET", "/api/v1/health", nil, &out); err != nil {
		return err
	}
	if !out.DatabaseConnectivity {
		return errors.New("headscale: server reports no database connectivity")
	}
	return nil
}

func etagOf(policy string) string {
	h := sha256.Sum256([]byte(policy))
	return `"` + hex.EncodeToString(h[:16]) + `"`
}

// isNoPolicy reports Headscale's answer for a database-mode server whose
// policy was never set.
func isNoPolicy(err error) bool {
	var e *tsapi.APIError
	return errors.As(err, &e) && strings.Contains(e.Message, "acl policy not found")
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c.BaseURL == "" || c.APIKey == "" {
		return errors.New("headscale: BaseURL and APIKey are required")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("headscale api %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		if m.Message == "" {
			m.Message = http.StatusText(resp.StatusCode)
		}
		return &tsapi.APIError{Status: resp.StatusCode, Message: m.Message, API: "headscale api"}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}
