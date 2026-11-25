//go:build e2evm

package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/r2/r2test"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestE2EVMR2 runs the R2 binding (ADR-036) on the microVM tier:
// the platform worker (R2Backend, the bucket's Store object) runs in the
// guest, and the bytes and the index cross the guest NIC to hostd, which
// holds the S3 backend (a hermetic, signature-checking S3 server). No
// backend address or credential enters the guest. Covers put/get/head,
// metadata, a ranged get, onlyIf, list, delete and multipart, from the
// worker and from a facet-mode object, and a 64 MiB body streamed in and
// out.
func TestE2EVMR2(t *testing.T) {
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL and TW_VM_ROOTFS must be set (make e2e-vm / e2e-vz)")
	}
	be, _, err := newBackend(kernel, rootfs, 2, -1, false)
	if err != nil {
		t.Fatalf("microVM backend unavailable: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	fake := r2test.StartFakeS3(t.TempDir())
	defer fake.Close()

	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	e := hostdtest.Start(t, hostdtest.Options{ControlURL: ctl.URL, Fake: fakeStatic(), Ephemeral: true, KillTombstone: 5 * time.Second,
		VM: be, R2: fake.Backend("tw/"),
		Tenant: mgmtapi.Tenant{Tailnet: "ttest.ts.net", ControlURL: ctl.URL, MintMode: "static", Tier: bundle.TierMicroVM}})
	ctx := context.Background()
	client, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	hc := client.HTTPClient(2 * time.Minute)

	dep := e.Deploy(hostdtest.Fixture("r2probe"), "r2probe", nil)
	wl := e.WaitActive("r2probe", dep, 3*time.Minute)
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "r2probe" && w.Tier != bundle.TierMicroVM {
			t.Fatalf("r2probe runs in tier %q, want microvm", w.Tier)
		}
	}
	wc, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := client.WaitPeers(wc, wl.IP); err != nil {
		t.Fatal(err)
	}
	cancel()

	do := func(method, url string, body []byte) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, url, bytes.NewReader(body))
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	op := func(base string, a map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(a)
		code, out := do("POST", base+"/op", b)
		var m map[string]any
		json.Unmarshal(out, &m)
		if code != 200 {
			t.Fatalf("op %v: %d %s", a, code, out)
		}
		return m
	}
	for _, where := range []string{"", "/do"} {
		base := "http://" + wl.IP + where
		k := func(n string) string { return "vm" + where + "/" + n }
		put := op(base, map[string]any{"op": "put", "key": k("a"), "value": "hello vm", "opts": map[string]any{
			"httpMetadata": map[string]any{"contentType": "text/plain"}, "customMetadata": map[string]any{"who": "guest"}}})
		if put["size"] != float64(8) || put["etag"] == "" {
			t.Fatalf("%s put: %v", where, put)
		}
		g := op(base, map[string]any{"op": "get", "key": k("a"), "opts": map[string]any{"range": map[string]any{"offset": 6}}})
		if g["text"] != "vm" || g["httpMetadata"].(map[string]any)["contentType"] != "text/plain" || g["customMetadata"].(map[string]any)["who"] != "guest" {
			t.Fatalf("%s ranged get: %v", where, g)
		}
		if r := op(base, map[string]any{"op": "put", "key": k("a"), "value": "x", "opts": map[string]any{"onlyIf": map[string]any{"etagDoesNotMatch": "*"}}}); r != nil {
			t.Fatalf("%s put if absent over a key: %v", where, r)
		}
		if g := op(base, map[string]any{"op": "get", "key": k("a"), "opts": map[string]any{"onlyIf": map[string]any{"etagMatches": "nope"}}}); g["hasBody"] != false {
			t.Fatalf("%s get with a failed condition: %v", where, g)
		}
		mp := op(base, map[string]any{"op": "mpu", "key": k("mp"), "parts": []int{5 << 20, 10}})
		if mp["object"].(map[string]any)["size"] != float64(5<<20+10) {
			t.Fatalf("%s multipart: %v", where, mp)
		}
		ls := op(base, map[string]any{"op": "list", "opts": map[string]any{"prefix": k("")}})
		if n := len(ls["objects"].([]any)); n != 2 {
			t.Fatalf("%s list: %v", where, ls)
		}
		op(base, map[string]any{"op": "delete", "keys": []string{k("a"), k("mp")}})
		if h := op(base, map[string]any{"op": "head", "key": k("a")}); h != nil {
			t.Fatalf("%s head after delete: %v", where, h)
		}
		// 64 MiB in and out, streamed.
		body := bytes.Repeat([]byte("0123456789abcdef"), 4<<20)
		if code, out := do("PUT", base+"/stream?key="+k("big"), body); code != 200 {
			t.Fatalf("%s stream put: %d %s", where, code, out)
		}
		if code, back := do("GET", base+"/stream?key="+k("big"), nil); code != 200 || !bytes.Equal(back, body) {
			t.Fatalf("%s stream get: %d, %d bytes", where, code, len(back))
		}
	}
	if fake.Denied.Load() != 0 {
		t.Fatalf("fake S3 refused %d signatures", fake.Denied.Load())
	}
	for _, n := range fake.Keys() {
		if !strings.HasPrefix(n, "tw/t/"+e.TenantID+"/w/r2probe/b/main/") {
			t.Fatalf("blob %q outside the workload's bucket prefix", n)
		}
	}
	t.Logf("R2 on the microVM tier: ok (%s, %d blobs, %d S3 puts)", be.Name(), len(fake.Keys()), fake.Puts.Load())
}
