package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCLIR2: bucket create / ls / rm on the stores API, and object ls (mgmt
// folds the bucket's journal: puts and deletes by key, as the platform's
// R2 kind writes them, into the live keys).
func TestCLIR2(t *testing.T) {
	e := newPubEnv(t)
	e.setPolicy(`{}`)
	app := writePlainApp(t)
	if out, err := e.call(t, cmdDeploy, app, "--allow-missing-grants", "--no-wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdR2, "bucket", "ls", "--workload", "support"); err != nil || !strings.Contains(out, "no R2 buckets") {
		t.Fatalf("ls empty: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdR2, "bucket", "create", "photos", "--workload", "support"); err != nil || !strings.Contains(out, "bucket photos ready") {
		t.Fatalf("create: %v\n%s", err, out)
	}
	ctx := context.Background()
	entry := func(o map[string]any) mgmt.StoreEntryIn {
		b, _ := json.Marshal(o)
		return mgmt.StoreEntryIn{CKey: "o:" + o["k"].(string), Data: b}
	}
	row := func(size int, ct string) map[string]any {
		return map[string]any{"version": "v", "size": size, "etag": "e", "uploaded": 1, "checksums": map[string]any{}, "http": map[string]any{"contentType": ct}, "custom": map[string]any{}, "blobs": []any{}}
	}
	if _, err := e.m.AppendStore(ctx, e.tenantID, "support", "r2", "photos", "", nil, 0, "", []mgmt.StoreEntryIn{
		entry(map[string]any{"o": "put", "k": "cats/a.jpg", "row": row(2048, "image/jpeg")}),
		entry(map[string]any{"o": "put", "k": "cats/b.jpg", "row": row(10, "image/jpeg")}),
		entry(map[string]any{"o": "put", "k": "dogs/c.jpg", "row": row(3, "image/jpeg")}),
		entry(map[string]any{"o": "del", "k": "cats/b.jpg"}),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := e.call(t, cmdR2, "object", "ls", "photos", "--workload", "support", "--json")
	var r r2Result
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || len(r.Objects) != 2 || r.Objects[0].Key != "cats/a.jpg" || r.Objects[0].Size != 2048 || r.Objects[0].ContentType != "image/jpeg" {
		t.Fatalf("object ls: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdR2, "object", "ls", "photos", "--prefix", "dogs/", "--workload", "support"); err != nil || !strings.Contains(out, "dogs/c.jpg") || strings.Contains(out, "cats/") {
		t.Fatalf("object ls --prefix: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdR2, "bucket", "ls", "--workload", "support"); err != nil || !strings.Contains(out, "photos") {
		t.Fatalf("bucket ls: %v\n%s", err, out)
	}
	if _, err := e.call(t, cmdR2, "object", "get", "photos", "k", "--workload", "support"); cliCode(err) != "not_supported" {
		t.Fatalf("object get: %v", err)
	}
	if _, err := e.call(t, cmdR2, "bucket", "create", "a/b", "--workload", "support"); cliCode(err) != "bad_request" {
		t.Fatalf("bad bucket name: %v", err)
	}
	if out, err := e.call(t, cmdR2, "bucket", "rm", "photos", "--workload", "support", "--yes"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdR2, "object", "ls", "photos", "--workload", "support"); err != nil || !strings.Contains(out, "no objects") {
		t.Fatalf("object ls after rm: %v\n%s", err, out)
	}
}
