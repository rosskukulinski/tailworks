package mgmt

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/r2wire"
)

// TestR2Objects: GET …/stores/r2/{name}/objects folds a bucket's index
// (entries as store-r2.js writes them, the first one creating the bucket) into its live objects, by prefix,
// one page at a time; multipart state is skipped and an entry from a
// newer tailworks is a version_skew, never skipped.
func TestR2Objects(t *testing.T) {
	t.Parallel()
	s := newStoreEnv(t)
	c, h1 := s.e.c, s.e.asHost("h1")
	base := "/v1/tenants/" + s.tenant + "/workloads/support/stores/r2/photos"

	c.fail("GET", base+"/objects", nil, 404, "unknown_store")
	var pg struct {
		Items      []r2wire.Object `json:"items"`
		NextCursor *string         `json:"nextCursor"`
	}
	journal := fmt.Sprintf("%s/journal?epoch=%d", base, s.epoch)

	row := func(size int) map[string]any {
		return map[string]any{"version": "v1", "size": size, "etag": "e", "uploaded": 7, "checksums": map[string]any{}, "http": map[string]any{"contentType": "image/jpeg"}, "custom": map[string]any{}, "blobs": []any{}}
	}
	ent := func(ckey string, e map[string]any) map[string]any {
		b, _ := json.Marshal(e)
		return map[string]any{"ckey": ckey, "data": b}
	}
	var r StoreAppendResult
	h1.ok("POST", journal, map[string]any{"after": 0, "entries": []any{
		ent("o:cats/a", map[string]any{"v": 1, "o": "put", "k": "cats/a", "row": row(1)}),
		ent("o:cats/b", map[string]any{"v": 1, "o": "put", "k": "cats/b", "row": row(2)}),
		ent("o:cats/c", map[string]any{"o": "put", "k": "cats/c", "row": row(3)}), // v0: no v
		ent("u:u1", map[string]any{"v": 1, "o": "mpu", "id": "u1", "k": "dogs/x", "http": map[string]any{}, "custom": map[string]any{}, "created": 1}),
		ent("p:u1:1", map[string]any{"v": 1, "o": "part", "id": "u1", "n": 1, "blob": "b", "size": 5, "etag": "e", "md5": "m"}),
		ent("o:dogs/d", map[string]any{"v": 1, "o": "put", "k": "dogs/d", "row": row(4)}),
		ent("o:cats/b", map[string]any{"v": 1, "o": "del", "k": "cats/b"}),
	}}, &r)

	c.ok("GET", base+"/objects", nil, &pg)
	if len(pg.Items) != 3 || pg.Items[0].Key != "cats/a" || pg.Items[1].Key != "cats/c" || pg.Items[2].Key != "dogs/d" || pg.Items[2].Size != 4 || pg.Items[0].ContentType != "image/jpeg" {
		t.Fatalf("list: %+v", pg.Items)
	}
	var keys []string
	cursor := ""
	for {
		c.ok("GET", base+"/objects?prefix=cats/&limit=1&cursor="+cursor, nil, &pg)
		for _, o := range pg.Items {
			keys = append(keys, o.Key)
		}
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	if len(keys) != 2 || keys[0] != "cats/a" || keys[1] != "cats/c" {
		t.Fatalf("paged: %v", keys)
	}
	c.fail("GET", base+"/objects?cursor=nope", nil, 400, "bad_cursor")

	h1.ok("POST", journal, map[string]any{"after": r.Head, "incarnation": r.Incarnation, "entries": []any{
		ent("o:z", map[string]any{"v": 2, "o": "put", "k": "z", "row": row(1)}),
	}}, nil)
	c.fail("GET", base+"/objects", nil, 409, "version_skew")
}
