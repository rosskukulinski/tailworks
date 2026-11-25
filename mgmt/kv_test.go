package mgmt

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/kvwire"
)

// TestKVAdmin: the operator's KV view (tw kv key …) reads and writes the
// same journal a binding does, with Cloudflare's rules; a host can't use
// it (TestV1RoleMatrix); a namespace must exist; entries written by a binding (kv.js) are
// read back, and admin writes are entries a binding can apply.
func TestKVAdmin(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	c, h1 := s.e.c, s.e.asHost("h1")
	base := "/v1/tenants/" + s.tenant + "/workloads/support/stores/kv/sessions"
	val := func(k string) string { return base + "/value?key=" + url.QueryEscape(k) }

	c.fail("GET", base+"/keys", nil, 404, "unknown_store")
	c.fail("PUT", val("a"), map[string]any{"value": []byte("1")}, 404, "unknown_store")
	c.ok("PUT", "/v1/tenants/"+s.tenant+"/workloads/support/stores/kv/sessions", nil, nil)

	// A binding's write (as kv.js encodes it) is visible to the admin.
	var r StoreAppendResult
	h1.ok("POST", s.journal(s.epoch, ""), map[string]any{"after": 0, "entries": []any{
		map[string]any{"ckey": "from/worker", "data": kvwire.Encode(kvwire.Header{Key: "from/worker", Metadata: `{"by":"worker"}`}, []byte("w"))},
	}}, &r)
	var v KVValue
	c.ok("GET", val("from/worker"), nil, &v)
	if string(v.Value) != "w" || string(v.Metadata) != `{"by":"worker"}` {
		t.Fatalf("get: %+v %s", v, v.Metadata)
	}
	if strings.Contains(v.String(), "w}") {
		t.Fatal("KVValue.String prints the value")
	}

	// Admin writes, with metadata and expirations, and the rules.
	now := time.Now().Unix()
	var k KVKey
	c.ok("PUT", val("a b/ü"), map[string]any{"value": []byte("one"), "metadata": map[string]any{"tag": "<x>"}, "expirationTtl": 120}, &k)
	var meta map[string]string
	json.Unmarshal(k.Metadata, &meta)
	if k.Expiration < now+120 || meta["tag"] != "<x>" {
		t.Fatalf("put: %+v %s", k, k.Metadata)
	}
	c.ok("PUT", val("z"), map[string]any{"value": []byte("last"), "expiration": now + 3600}, nil)
	for i := 0; i < 5; i++ {
		c.ok("PUT", val(fmt.Sprintf("p/%d", i)), map[string]any{"value": []byte("x")}, nil)
	}
	c.fail("PUT", val("t"), map[string]any{"value": []byte("x"), "expirationTtl": 10}, 400, "bad_request")
	c.fail("PUT", val("t"), map[string]any{"value": []byte("x"), "expiration": now + 5}, 400, "bad_request")
	c.fail("PUT", val("t"), map[string]any{"value": []byte("x"), "metadata": strings.Repeat("m", 1100)}, 400, "bad_request")
	c.fail("PUT", val(".."), map[string]any{"value": []byte("x")}, 400, "bad_request")
	c.fail("PUT", val("t"), map[string]any{"value": make([]byte, kvwire.MaxValueBytes+1)}, 400, "store_limit")
	c.fail("GET", val("nope"), nil, 404, "not_found")
	// (Hosts are refused every KV route: TestV1RoleMatrix, from the spec.)

	// list: prefix, paging by cursor.
	var page KVKeyPage
	c.ok("GET", base+"/keys", nil, &page)
	if len(page.Keys) != 8 || !page.ListComplete || page.Keys[0].Name != "a b/ü" || page.Keys[7].Name != "z" {
		t.Fatalf("list: %+v", page)
	}
	var names []string
	cursor := ""
	for {
		c.ok("GET", base+"/keys?prefix=p/&limit=2&cursor="+cursor, nil, &page)
		for _, k := range page.Keys {
			names = append(names, k.Name)
		}
		if page.ListComplete {
			break
		}
		cursor = page.Cursor
	}
	if fmt.Sprint(names) != "[p/0 p/1 p/2 p/3 p/4]" {
		t.Fatalf("paged: %v", names)
	}
	c.fail("GET", base+"/keys?cursor=%25%25", nil, 400, "bad_request")

	// delete: written once; a missing key writes nothing.
	var st StoreInfo
	c.ok("GET", base, nil, &st)
	c.okStatus("DELETE", val("p/0"), nil, nil)
	c.okStatus("DELETE", val("p/0"), nil, nil)
	var st2 StoreInfo
	c.ok("GET", base, nil, &st2)
	if st2.Head != st.Head+1 {
		t.Fatalf("delete appended %d entries, want 1", st2.Head-st.Head)
	}
	c.fail("GET", val("p/0"), nil, 404, "not_found")

	// Admin writes are audited with the namespace, never the key.
	var au struct {
		Records []AuditRecord `json:"items"`
	}
	c.ok("GET", "/v1/audit?limit=500&tenant="+s.tenant, nil, &au)
	puts, dels := 0, 0
	for _, r := range au.Records {
		switch r.Kind {
		case "kv_put":
			puts++
		case "kv_delete":
			dels++
		default:
			continue
		}
		if b, _ := json.Marshal(r); strings.Contains(string(b), "a b/") || strings.Contains(string(b), "p/0") {
			t.Fatalf("an audit record names the key: %s", b)
		}
	}
	if puts != 7 || dels != 1 {
		t.Fatalf("audit: %d kv_put, %d kv_delete; want 7 and 1", puts, dels)
	}

	// Every admin entry is one a binding can apply (the journal is the
	// contract): it folds to what the admin lists.
	var p StoreJournalPage
	h1.ok("GET", s.journal(s.epoch, ""), nil, &p)
	data := make([][]byte, len(p.Entries))
	for i, e := range p.Entries {
		data[i] = e.Data
	}
	items, err := kvwire.Fold(data, time.Now().Unix())
	if err != nil || len(items) != 7 || string(items["z"].Value) != "last" {
		t.Fatalf("journal fold: %v %v", items, err)
	}
}
