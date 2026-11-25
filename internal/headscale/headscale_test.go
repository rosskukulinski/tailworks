package headscale

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// fake mimics Headscale v0.29's REST gateway (responses as observed from
// a real server: camelCase fields, uint64 IDs as strings, gRPC errors as
// {"code","message","details"}).
type fake struct {
	mu      sync.Mutex
	policy  string
	expired []string
	deleted []string
	bodies  []map[string]any
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer hskey-api-test" {
		w.WriteHeader(401)
		io.WriteString(w, `{"code":16,"message":"Unauthorized"}`)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.bodies = append(f.bodies, body)
	grpcErr := func(status, code int, msg string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "details": []any{}})
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v1/preauthkey":
		io.WriteString(w, `{"preAuthKey":{"user":null,"id":"7","key":"hskey-auth-secret","reusable":false,"ephemeral":false,"used":false,"aclTags":["tag:tw"]}}`)
	case r.Method == "POST" && r.URL.Path == "/api/v1/preauthkey/expire":
		if body["id"] == "404" {
			grpcErr(404, 5, "not found")
			return
		}
		f.expired = append(f.expired, body["id"].(string))
		io.WriteString(w, `{}`)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/node/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
		if id == "404" {
			grpcErr(404, 5, "node not found")
			return
		}
		f.deleted = append(f.deleted, id)
		io.WriteString(w, `{}`)
	case r.Method == "GET" && r.URL.Path == "/api/v1/node":
		io.WriteString(w, `{"nodes":[{"id":"3","name":"tw-room-abc","givenName":"tw-room-abc","tags":["tag:tw-agent"]}]}`)
	case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
		if f.policy == "" {
			grpcErr(500, 2, "loading ACL from database: acl policy not found")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"policy": f.policy, "updatedAt": "2026-11-15T00:00:00Z"})
	case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
		f.policy = body["policy"].(string)
		json.NewEncoder(w).Encode(map[string]any{"policy": f.policy})
	case r.Method == "POST" && r.URL.Path == "/api/v1/policy/check":
		if strings.Contains(body["policy"].(string), "postures") {
			grpcErr(400, 3, `parsing policy: unknown field: "postures"`)
			return
		}
		io.WriteString(w, `{}`)
	default:
		grpcErr(404, 5, "Not Found")
	}
}

func newClient(t *testing.T) (*Client, *fake) {
	f := &fake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return &Client{BaseURL: srv.URL + "/", APIKey: "hskey-api-test", BaseDomain: "hs.example.test", Now: func() time.Time { return now }}, f
}

func TestCreateKey(t *testing.T) {
	c, f := newClient(t)
	k, err := c.CreateKey(context.Background(), tsapi.KeyOptions{Tags: []string{"tag:tw"}, Preauthorized: true, ExpirySeconds: 300, Description: "tw o_1"})
	if err != nil {
		t.Fatal(err)
	}
	if k.ID != "7" || k.Secret != "hskey-auth-secret" {
		t.Fatalf("key = %+v", k)
	}
	b := f.bodies[0]
	if b["expiration"] != "2026-11-15T12:05:00Z" || b["reusable"] != false || b["ephemeral"] != false {
		t.Errorf("body = %v", b)
	}
	if tags, _ := b["aclTags"].([]any); len(tags) != 1 || tags[0] != "tag:tw" {
		t.Errorf("aclTags = %v", b["aclTags"])
	}
	if _, ok := b["user"]; ok {
		t.Errorf("tagged keys carry no user: %v", b)
	}
	if strings.Contains(k.String(), "secret") {
		t.Errorf("Key.String leaks the secret")
	}
}

func TestDeleteKeyAndDevice(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if err := c.DeleteKey(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteKey(ctx, "404"); err != nil {
		t.Errorf("DeleteKey(missing) = %v, want nil", err)
	}
	if err := c.DeleteKey(ctx, "k123"); err == nil {
		t.Errorf("DeleteKey accepted a non-numeric id")
	}
	if err := c.DeleteDevice(ctx, "3"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDevice(ctx, "404"); err != nil {
		t.Errorf("DeleteDevice(missing) = %v, want nil", err)
	}
	if err := c.DeleteDevice(ctx, "../user/1"); err == nil {
		t.Errorf("DeleteDevice accepted a non-numeric id")
	}
	if len(f.expired) != 1 || f.expired[0] != "7" || len(f.deleted) != 1 || f.deleted[0] != "3" {
		t.Errorf("expired %v deleted %v", f.expired, f.deleted)
	}
}

func TestListDevices(t *testing.T) {
	c, _ := newClient(t)
	devs, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].ID != "3" || devs[0].NodeID != "3" || devs[0].Name != "tw-room-abc.hs.example.test" || devs[0].Tags[0] != "tag:tw-agent" {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	// No policy yet: an empty one, no ETag.
	p, etag, err := c.GetPolicyHuJSON(ctx)
	if err != nil || string(p) != "{}" || etag != "" {
		t.Fatalf("empty policy = %q %q %v", p, etag, err)
	}
	if err := c.ValidatePolicy(ctx, []byte(`{"postures":{}}`)); err == nil || !strings.Contains(err.Error(), "headscale api: 400") {
		t.Errorf("ValidatePolicy(postures) = %v", err)
	}
	next := "{\n  // tw\n  \"tagOwners\": {\"tag:tw\": []},\n}"
	ne, err := c.SetPolicy(ctx, []byte(next), "")
	if err != nil {
		t.Fatal(err)
	}
	if f.policy != next {
		t.Fatalf("stored %q", f.policy)
	}
	// A stale ETag is refused with a 412.
	if _, err := c.SetPolicy(ctx, []byte("{}"), ""); err == nil || !strings.Contains(err.Error(), "412") {
		t.Errorf("SetPolicy(stale) = %v", err)
	}
	// GetPolicy returns standard JSON and honours the ETag.
	std, e2, nm, err := c.GetPolicy(ctx, "")
	if err != nil || nm || e2 != ne || strings.Contains(string(std), "//") {
		t.Fatalf("GetPolicy = %q %q %v %v", std, e2, nm, err)
	}
	if _, _, nm, err := c.GetPolicy(ctx, ne); err != nil || !nm {
		t.Errorf("GetPolicy(etag) notModified = %v, %v", nm, err)
	}
}

func TestErrorsDontLeakTheAPIKey(t *testing.T) {
	c, _ := newClient(t)
	c.APIKey = "hskey-api-wrong"
	_, err := c.CreateKey(context.Background(), tsapi.KeyOptions{Tags: []string{"tag:tw"}})
	if !tsapi.IsUnauthorized(err) || strings.Contains(err.Error(), "hskey-api-wrong") {
		t.Fatalf("err = %v", err)
	}
}

func TestTooOld(t *testing.T) {
	// Headscale v0.28 has no /api/v1/policy/check: the gateway's 404.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"code":5,"message":"Not Found","details":[]}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	if err := c.CheckVersion(context.Background()); !errors.Is(err, ErrTooOld) {
		t.Fatalf("CheckVersion = %v, want ErrTooOld", err)
	}
	c2, _ := newClient(t)
	if err := c2.CheckVersion(context.Background()); err != nil {
		t.Fatalf("CheckVersion on v0.29 = %v", err)
	}
}
