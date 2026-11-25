package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/kvwire"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// kvCaller drives the kv fixture's /op (worker) and /do/op (facet).
type kvCaller struct {
	t  *testing.T
	s  *suite
	ip string
}

// op runs env.<binding>.<op>(...args) in the worker ("/op") or a facet
// ("/do/op") and returns its serialized result, or the error message.
func (c *kvCaller) op(path, binding, op string, args ...any) (any, string) {
	c.t.Helper()
	if args == nil {
		args = []any{}
	}
	body, _ := json.Marshal(map[string]any{"binding": binding, "op": op, "args": args})
	code, _, b := c.s.do("POST", "http://"+c.ip+path, body, "content-type", "application/json")
	var m struct {
		R     any    `json:"r"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatalf("%s %s %v: %d %s", path, op, args, code, b)
	}
	return m.R, m.Error
}

// must is op that fails the test on an error.
func (c *kvCaller) must(path, binding, op string, args ...any) any {
	c.t.Helper()
	r, err := c.op(path, binding, op, args...)
	if err != "" {
		c.t.Fatalf("%s %s.%s%v: %s", path, binding, op, args, err)
	}
	return r
}

// fails is op that must fail with an error containing want.
func (c *kvCaller) fails(want, path, binding, op string, args ...any) {
	c.t.Helper()
	r, err := c.op(path, binding, op, args...)
	if !strings.Contains(err, want) {
		c.t.Fatalf("%s %s.%s%v: %v %q, want an error with %q", path, binding, op, args, r, err, want)
	}
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	var gn, wn any
	json.Unmarshal(g, &gn)
	json.Unmarshal(w, &wn)
	if !reflect.DeepEqual(gn, wn) {
		t.Fatalf("%s: %s, want %s", what, g, w)
	}
}

func kvJournal(e *hostdtest.Env, workload, name string) mgmtapi.StoreJournalPage {
	e.T.Helper()
	var p mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(context.Background(), "GET", "/v1/tenants/"+e.TenantID+"/workloads/"+workload+"/stores/kv/"+name+"/journal", nil, &p); err != nil {
		e.T.Fatal(err)
	}
	return p
}

// TestWorkersKV runs the Workers KV binding (ADR-037) on real
// workerd: Cloudflare's API through workerd's own kvNamespace binding, in
// the worker and in a facet, on the platform-store substrate. It covers
// get types, metadata, TTL validation and expiry (with the alarm that
// journals the delete), list with prefix/limit/cursor, bulk get, limits,
// namespace sharing between bindings, and one journal entry per write.
func TestWorkersKV(t *testing.T) {
	t.Parallel()
	testWorkersKV(t, hostdtest.BackendFake)
}

// testWorkersKV is TestWorkersKV against backend (realmgmt_test.go).
func testWorkersKV(t *testing.T, backend string) {
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, onBackend(backend, nil))
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("kv"), "kv", nil)
	wl := e.WaitActive("kv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	c := &kvCaller{t: t, s: s, ip: wl.IP}
	const W, F = "/op", "/do/op"

	t.Run("GetPutTypes", func(t *testing.T) {
		c.t = t
		eq(t, "missing", c.must(W, "KV", "get", "nope"), nil)
		c.must(W, "KV", "put", "greeting", "hello")
		eq(t, "get", c.must(W, "KV", "get", "greeting"), "hello")
		eq(t, "get text", c.must(W, "KV", "get", "greeting", "text"), "hello")
		c.must(W, "KV", "put", "doc", `{"a":[1,2]}`)
		eq(t, "get json", c.must(W, "KV", "get", "doc", map[string]any{"type": "json"}), map[string]any{"a": []int{1, 2}})
		c.must(W, "KV", "putBytes", "bin", []int{0, 1, 2, 255})
		eq(t, "get arrayBuffer", c.must(W, "KV", "get", "bin", "arrayBuffer"), map[string]any{"bytes": []int{0, 1, 2, 255}})
		c.must(W, "KV", "putStream", "streamed", "from a stream")
		eq(t, "get stream", c.must(W, "KV", "get", "streamed", "stream"), map[string]any{"stream": "from a stream"})
		c.must(W, "KV", "put", "empty", "")
		eq(t, "empty value", c.must(W, "KV", "get", "empty"), "")
		// Keys are arbitrary UTF-8 (URL-encoded on the wire).
		odd := "a/b c?d=e&f%20é#"
		c.must(W, "KV", "put", odd, "odd")
		eq(t, "odd key", c.must(W, "KV", "get", odd), "odd")
		eq(t, "get json of text", c.fails2(W, "KV", "get", "greeting", "json"), "Unexpected token")
	})

	t.Run("Metadata", func(t *testing.T) {
		c.t = t
		c.must(W, "KV", "put", "m1", "v", map[string]any{"metadata": map[string]any{"owner": "ross", "n": 3}})
		eq(t, "getWithMetadata", c.must(W, "KV", "getWithMetadata", "m1"),
			map[string]any{"value": "v", "metadata": map[string]any{"owner": "ross", "n": 3}, "cacheStatus": nil})
		eq(t, "no metadata", c.must(W, "KV", "getWithMetadata", "greeting"), map[string]any{"value": "hello", "metadata": nil, "cacheStatus": nil})
		eq(t, "missing", c.must(W, "KV", "getWithMetadata", "nope"), map[string]any{"value": nil, "metadata": nil, "cacheStatus": nil})
		c.fails("exceeds limit of 1024", W, "KV", "put", "m2", "v", map[string]any{"metadata": map[string]any{"x": strings.Repeat("y", 1100)}})
	})

	t.Run("TTL", func(t *testing.T) {
		c.t = t
		c.fails("Expiration TTL must be at least 60", W, "KV", "put", "t", "v", map[string]any{"expirationTtl": 10})
		c.fails("at least 60 seconds in the future", W, "KV", "put", "t", "v", map[string]any{"expiration": time.Now().Unix() + 5})
		c.fails("greater than the current", W, "KV", "put", "t", "v", map[string]any{"expiration": time.Now().Unix() - 5})
		before := time.Now().Unix()
		c.must(W, "KV", "put", "ttl/a", "v", map[string]any{"expirationTtl": 120, "metadata": map[string]any{"k": 1}})
		abs := time.Now().Unix() + 3600
		c.must(W, "KV", "put", "ttl/b", "v", map[string]any{"expiration": abs})
		l := c.must(W, "KV", "list", map[string]any{"prefix": "ttl/"}).(map[string]any)
		keys := l["keys"].([]any)
		if len(keys) != 2 {
			t.Fatalf("list ttl/: %v", l)
		}
		a, b := keys[0].(map[string]any), keys[1].(map[string]any)
		if x := int64(a["expiration"].(float64)); x < before+120 || x > time.Now().Unix()+120 {
			t.Fatalf("ttl/a expiration %d, want now+120", x)
		}
		eq(t, "ttl/a metadata", a["metadata"], map[string]any{"k": 1})
		eq(t, "ttl/b", b, map[string]any{"name": "ttl/b", "expiration": abs})

		// Expiry: an admin writes a key that expires in a few seconds
		// straight into the journal (the 60 s minimum is the binding's, as
		// on Cloudflare). The worker's next write conflicts and catches up.
		//
		// The object's alarm journals the delete once the key expires, so
		// every host (and a catch-up from scratch) drops the key too.
		dropped := func() {
			t.Helper()
			deadline := time.Now().Add(10 * time.Second)
			for {
				items, err := kvFold(kvJournal(e, "kv", "main"), 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := items["ttl/soon"]; !ok {
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the expired key was not deleted from the journal")
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		// A null read before expiry is a bug only if the read finished
		// before the key expired. On a loaded runner the append, the
		// catch-up and the read can outlast the window (expiration is whole
		// seconds: "3 s" is 2 to 3 s). Then, once the alarm has journalled
		// the delete, the key is written again with a wider window (#392).
		var exp int64
		for window := int64(3); ; window *= 2 {
			p := kvJournal(e, "kv", "main")
			exp = time.Now().Unix() + window
			var ar mgmtapi.StoreAppendResult
			if err := e.Mgmt.Do(context.Background(), "POST", "/v1/tenants/"+e.TenantID+"/workloads/kv/stores/kv/main/journal", map[string]any{
				"after": p.Head, "incarnation": p.Incarnation,
				"entries": []any{map[string]any{"ckey": "ttl/soon", "data": kvwire.Encode(kvwire.Header{Key: "ttl/soon", Expiration: exp}, []byte("brief"))}},
			}, &ar); err != nil {
				t.Fatal(err)
			}
			c.must(W, "KV", "put", "poke", "1")
			got := c.must(W, "KV", "get", "ttl/soon")
			if got == "brief" {
				break
			}
			if time.Now().Unix() < exp || window >= 24 {
				eq(t, "before expiry", got, "brief")
			}
			t.Logf("the read finished after the key's %d s expiry; again with a wider window", window)
			dropped()
		}
		time.Sleep(time.Until(time.Unix(exp, 0)) + 1100*time.Millisecond)
		eq(t, "after expiry", c.must(W, "KV", "get", "ttl/soon"), nil)
		eq(t, "after expiry, facet", c.must(F, "KV", "get", "ttl/soon"), nil)
		if names := c.must(W, "KV", "listAll", map[string]any{"prefix": "ttl/"}).(map[string]any)["names"]; fmt.Sprint(names) != "[ttl/a ttl/b]" {
			t.Fatalf("list after expiry: %v", names)
		}
		dropped()
	})

	t.Run("ListPaging", func(t *testing.T) {
		c.t = t
		eq(t, "fill", c.must(W, "KV", "fill", "page/", 25), 25)
		r := c.must(W, "KV", "listAll", map[string]any{"prefix": "page/", "limit": 10}).(map[string]any)
		eq(t, "pages", r["pages"], []int{10, 10, 5})
		names := r["names"].([]any)
		if len(names) != 25 || names[0] != "page/000" || names[24] != "page/024" {
			t.Fatalf("names: %v", names)
		}
		one := c.must(W, "KV", "list", map[string]any{"prefix": "page/", "limit": 3}).(map[string]any)
		if one["list_complete"] != false || one["cursor"] == "" || len(one["keys"].([]any)) != 3 {
			t.Fatalf("first page: %v", one)
		}
		last := c.must(W, "KV", "list", map[string]any{"prefix": "page/02"}).(map[string]any)
		if last["list_complete"] != true || last["cursor"] != nil || len(last["keys"].([]any)) != 5 {
			t.Fatalf("complete page: %v", last)
		}
		c.fails("key_count_limit", W, "KV", "list", map[string]any{"limit": 1001})
		c.fails("Invalid cursor", W, "KV", "list", map[string]any{"cursor": "!!!"})
	})

	t.Run("BulkGet", func(t *testing.T) {
		c.t = t
		eq(t, "bulk", c.must(W, "KV", "get", []string{"greeting", "doc", "nope"}),
			map[string]any{"map": map[string]any{"greeting": "hello", "doc": `{"a":[1,2]}`, "nope": nil}})
		eq(t, "bulk json", c.must(W, "KV", "get", []string{"doc"}, "json"), map[string]any{"map": map[string]any{"doc": map[string]any{"a": []int{1, 2}}}})
		eq(t, "bulk with metadata", c.must(W, "KV", "getWithMetadata", []string{"m1", "nope"}),
			map[string]any{"map": map[string]any{"m1": map[string]any{"value": "v", "metadata": map[string]any{"owner": "ross", "n": 3}}, "nope": nil}})
		keys := make([]string, 101)
		for i := range keys {
			keys[i] = fmt.Sprint("k", i)
		}
		c.fails("maximum of 100 keys", W, "KV", "get", keys)
		c.fails("non-json", W, "KV", "get", []string{"greeting"}, "json")
	})

	t.Run("DeleteAndLimits", func(t *testing.T) {
		c.t = t
		c.must(W, "KV", "delete", "greeting")
		eq(t, "deleted", c.must(W, "KV", "get", "greeting"), nil)
		n := e.Hooks.StoreAppends()
		c.must(W, "KV", "delete", "never-existed")
		if e.Hooks.StoreAppends() != n {
			t.Fatal("deleting a key that does not exist wrote to the journal")
		}
		c.fails("Key name cannot be empty", W, "KV", "get", "")
		c.fails("exceeds key length limit of 512", W, "KV", "put", strings.Repeat("k", 513), "v")
		c.must(W, "KV", "putSize", "big", kvwire.MaxValueBytes)
		c.fails("413", W, "KV", "putSize", "too-big", kvwire.MaxValueBytes+1)
		if r := c.must(W, "KV", "get", "big"); len(r.(string)) != kvwire.MaxValueBytes {
			t.Fatalf("big value: %d bytes", len(r.(string)))
		}
		c.must(W, "KV", "delete", "big")
	})

	t.Run("FacetsAndSharing", func(t *testing.T) {
		c.t = t
		// A facet sees the worker's writes and vice versa, with the same API.
		eq(t, "facet get", c.must(F, "KV", "get", "doc", "json"), map[string]any{"a": []int{1, 2}})
		c.must(F, "KV", "put", "from-facet", "f", map[string]any{"metadata": map[string]any{"by": "facet"}})
		eq(t, "worker reads facet write", c.must(W, "KV", "getWithMetadata", "from-facet"),
			map[string]any{"value": "f", "metadata": map[string]any{"by": "facet"}, "cacheStatus": nil})
		eq(t, "facet bulk", c.must(F, "KV", "get", []string{"from-facet", "nope"}), map[string]any{"map": map[string]any{"from-facet": "f", "nope": nil}})
		eq(t, "facet arrayBuffer", c.must(F, "KV", "get", "bin", "arrayBuffer"), map[string]any{"bytes": []int{0, 1, 2, 255}})
		eq(t, "facet stream", c.must(F, "KV", "get", "streamed", "stream"), map[string]any{"stream": "from a stream"})
		c.must(F, "KV", "putStream", "facet-stream", "streamed by a facet")
		eq(t, "facet put stream", c.must(W, "KV", "get", "facet-stream"), "streamed by a facet")
		c.fails("Expiration TTL must be at least 60", F, "KV", "put", "t", "v", map[string]any{"expirationTtl": 1})
		if l := c.must(F, "KV", "listAll", map[string]any{"prefix": "page/", "limit": 7}).(map[string]any); fmt.Sprint(l["pages"]) != "[7 7 7 4]" {
			t.Fatalf("facet list pages: %v", l["pages"])
		}
		c.must(F, "KV", "delete", "from-facet")
		eq(t, "facet delete", c.must(W, "KV", "get", "from-facet"), nil)
		// KV2 binds the same namespace ("main"); OTHER is its own.
		eq(t, "KV2 shares main", c.must(W, "KV2", "get", "doc"), `{"a":[1,2]}`)
		eq(t, "OTHER is separate", c.must(W, "OTHER", "get", "doc"), nil)
		c.must(F, "OTHER", "put", "o", "other")
		eq(t, "OTHER", c.must(W, "OTHER", "get", "o"), "other")
		eq(t, "not in main", c.must(W, "KV", "get", "o"), nil)
	})

	t.Run("Journal", func(t *testing.T) {
		c.t = t
		// The journal (as mgmt keeps it) folds to exactly what the worker sees.
		items, err := kvFold(kvJournal(e, "kv", "main"), time.Now().Unix())
		if err != nil {
			t.Fatal(err)
		}
		r := c.must(W, "KV", "listAll", map[string]any{}).(map[string]any)
		var names []string
		for _, n := range r["names"].([]any) {
			names = append(names, n.(string))
			if _, ok := items[n.(string)]; !ok {
				t.Errorf("%s is listed but not in the journal", n)
			}
		}
		if len(names) != len(items) {
			t.Fatalf("worker lists %d keys, the journal has %d", len(names), len(items))
		}
		if string(items["doc"].Value) != `{"a":[1,2]}` {
			t.Fatalf("journal doc: %q", items["doc"].Value)
		}
		var st struct{ Head int64 }
		if err := e.Mgmt.Do(context.Background(), "GET", "/v1/tenants/"+e.TenantID+"/workloads/kv/stores/kv/OTHER", nil, &st); err != nil || st.Head != 1 {
			t.Fatalf("store kv/OTHER: %+v %v", st, err)
		}
	})
}

// fails2 is op returning only the error (for messages from workerd's
// client-side parsing).
func (c *kvCaller) fails2(path, binding, op string, args ...any) string {
	c.t.Helper()
	_, err := c.op(path, binding, op, args...)
	if err == "" {
		c.t.Fatalf("%s.%s%v did not fail", binding, op, args)
	}
	return err[:min(len(err), len("Unexpected token"))]
}

func kvFold(p mgmtapi.StoreJournalPage, now int64) (map[string]kvwire.Item, error) {
	if p.More {
		return nil, fmt.Errorf("journal page is partial")
	}
	data := make([][]byte, len(p.Entries))
	for i, e := range p.Entries {
		data[i] = e.Data
	}
	return kvwire.Fold(data, now)
}
