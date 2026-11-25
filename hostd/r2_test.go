package hostd_test

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/hostd/r2/r2test"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestR2Binding runs the R2 binding (ADR-036) on real workerd, from
// the worker and from a facet-mode object, against both blob backends:
// the fs backend and an S3 backend on a hermetic, signature-checking S3
// server. It covers put/get/head/list/delete, httpMetadata and
// customMetadata, checksums, ranged gets, onlyIf on get and put,
// multipart upload, and that blobs are named by tenant, workload and
// bucket.
func TestR2Binding(t *testing.T) {
	t.Parallel()
	testR2Binding(t, hostdtest.BackendFake)
}

// testR2Binding is TestR2Binding against backend (realmgmt_test.go).
func testR2Binding(t *testing.T, backend string) {
	fake := r2test.StartFakeS3(t.TempDir())
	defer fake.Close()
	for _, tc := range []struct {
		name string
		be   func(dir string) r2.Backend
	}{
		{"fs", func(dir string) r2.Backend { return &r2.FS{Root: dir} }},
		{"s3", func(string) r2.Backend { return fake.Backend("tw/") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blobDir := t.TempDir()
			s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, onBackend(backend, func(o *hostdtest.Options) {
				o.R2 = tc.be(blobDir)
			}))
			e := s.e
			dep := e.Deploy(hostdtest.Fixture("r2probe"), "r2probe", nil)
			wl := e.WaitActive("r2probe", dep, 60*time.Second)
			s.waitPeer(wl.IP)
			base := "http://" + wl.IP
			for _, where := range []string{"worker", "facet"} {
				pfx := ""
				if where == "facet" {
					pfx = "/do"
				}
				t.Run(where, func(t *testing.T) { r2Checks(t, s, base+pfx, where+"/") })
			}
			// Blobs are named by tenant, workload and bucket.
			var names []string
			if tc.name == "s3" {
				names = fake.Keys()
				if fake.Denied.Load() != 0 {
					t.Fatalf("fake S3 refused %d signatures", fake.Denied.Load())
				}
			} else {
				names = filesUnder(t, blobDir)
			}
			want := "t/" + e.TenantID + "/w/r2probe/b/"
			if tc.name == "s3" {
				want = "tw/" + want
			}
			if len(names) == 0 {
				t.Fatal("no blobs in the backend")
			}
			for _, n := range names {
				if !strings.HasPrefix(n, want+"main/") && !strings.HasPrefix(n, want+"other/") {
					t.Fatalf("blob %q is not under %s{main,other}/", n, want)
				}
			}
		})
	}
}

func r2op(t *testing.T, s *suite, base string, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	code, _, out := s.do("POST", base+"/op", b, "content-type", "application/json")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil && string(out) != "null" {
		t.Fatalf("op %v: %d %s", body, code, out)
	}
	if code != 200 {
		t.Fatalf("op %v: %d %s", body, code, out)
	}
	return m
}

func r2opErr(t *testing.T, s *suite, base string, body map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(body)
	code, _, out := s.do("POST", base+"/op", b, "content-type", "application/json")
	if code == 200 {
		t.Fatalf("op %v: succeeded: %s", body, out)
	}
	return string(out)
}

func r2Checks(t *testing.T, s *suite, base, kp string) {
	k := func(n string) string { return kp + n }
	sum := md5.Sum([]byte("hello, r2"))
	sha := sha256.Sum256([]byte("hello, r2"))

	t.Run("PutGetHeadMetadata", func(t *testing.T) {
		expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
		put := r2op(t, s, base, map[string]any{"op": "put", "key": k("hello.txt"), "value": "hello, r2", "opts": map[string]any{
			"httpMetadata":   map[string]any{"contentType": "text/plain", "contentLanguage": "en", "cacheControl": "max-age=60", "contentDisposition": "inline", "contentEncoding": "identity", "cacheExpiry": expiry},
			"customMetadata": map[string]any{"owner": "alice", "ünïcode": "✓"},
			"sha256Hex":      hex.EncodeToString(sha[:]),
		}})
		if put["etag"] != hex.EncodeToString(sum[:]) || put["httpEtag"] != `"`+hex.EncodeToString(sum[:])+`"` || put["size"] != float64(9) || put["version"] == "" {
			t.Fatalf("put: %v", put)
		}
		if cs := put["checksums"].(map[string]any); cs["md5"] != hex.EncodeToString(sum[:]) || cs["sha256"] != hex.EncodeToString(sha[:]) {
			t.Fatalf("put checksums: %v", cs)
		}
		// A matching MD5 is accepted (one hash per put, as on Cloudflare).
		r2op(t, s, base, map[string]any{"op": "put", "key": k("md5.txt"), "value": "hello, r2", "opts": map[string]any{"md5": hex.EncodeToString(sum[:])}})
		got := r2op(t, s, base, map[string]any{"op": "get", "key": k("hello.txt")})
		if got["text"] != "hello, r2" || got["storageClass"] != "Standard" || got["etag"] != put["etag"] || got["version"] != put["version"] || got["uploaded"] != put["uploaded"] {
			t.Fatalf("get: %v (put %v)", got, put)
		}
		hm := got["httpMetadata"].(map[string]any)
		if hm["contentType"] != "text/plain" || hm["contentLanguage"] != "en" || hm["cacheControl"] != "max-age=60" || hm["cacheExpiry"] != float64(expiry) {
			t.Fatalf("httpMetadata: %v", hm)
		}
		if h := got["headers"].(map[string]any); h["content-type"] != "text/plain" || h["cache-control"] != "max-age=60" || h["expires"] != "Wed, 02 Jan 2030 03:04:05 GMT" {
			t.Fatalf("writeHttpMetadata: %v", h)
		}
		if cm := got["customMetadata"].(map[string]any); cm["owner"] != "alice" || cm["ünïcode"] != "✓" {
			t.Fatalf("customMetadata: %v", cm)
		}
		head := r2op(t, s, base, map[string]any{"op": "head", "key": k("hello.txt")})
		if head["hasBody"] != false || head["etag"] != put["etag"] || head["customMetadata"].(map[string]any)["owner"] != "alice" {
			t.Fatalf("head: %v", head)
		}
		// httpMetadata as Headers.
		r2op(t, s, base, map[string]any{"op": "put", "key": k("h.json"), "value": "{}", "opts": map[string]any{"httpHeaders": map[string]string{"content-type": "application/json"}}})
		if g := r2op(t, s, base, map[string]any{"op": "head", "key": k("h.json")}); g["httpMetadata"].(map[string]any)["contentType"] != "application/json" {
			t.Fatalf("Headers httpMetadata: %v", g)
		}
		// Missing keys.
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("missing")}); g != nil {
			t.Fatalf("get missing: %v", g)
		}
		if g := r2op(t, s, base, map[string]any{"op": "head", "key": k("missing")}); g != nil {
			t.Fatalf("head missing: %v", g)
		}
		// Binary values, an empty value, a key that isn't a path.
		r2op(t, s, base, map[string]any{"op": "put", "key": k("../../etc/passwd ☃"), "b64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 255})})
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("../../etc/passwd ☃")}); g["size"] != float64(4) {
			t.Fatalf("odd key: %v", g)
		}
		r2op(t, s, base, map[string]any{"op": "put", "key": k("empty"), "value": ""})
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("empty")}); g["size"] != float64(0) || g["text"] != "" || g["hasBody"] != true {
			t.Fatalf("empty: %v", g)
		}
		// A wrong digest stores nothing.
		bad := md5.Sum([]byte("other"))
		if e := r2opErr(t, s, base, map[string]any{"op": "put", "key": k("bad"), "value": "x", "opts": map[string]any{"md5": hex.EncodeToString(bad[:])}}); !strings.Contains(e, "MD5") {
			t.Fatalf("bad md5: %s", e)
		}
		if g := r2op(t, s, base, map[string]any{"op": "head", "key": k("bad")}); g != nil {
			t.Fatalf("bad digest stored: %v", g)
		}
		if e := r2opErr(t, s, base, map[string]any{"op": "put", "key": k("big-meta"), "value": "x", "opts": map[string]any{"customMetadata": map[string]any{"m": strings.Repeat("x", 8200)}}}); !strings.Contains(e, "metadata") {
			t.Fatalf("metadata too large: %s", e)
		}
		if e := r2opErr(t, s, base, map[string]any{"op": "put", "key": strings.Repeat("k", 1025), "value": "x"}); !strings.Contains(strings.ToLower(e), "name") {
			t.Fatalf("key too long: %s", e)
		}
		// Overwrite: new version and etag.
		put2 := r2op(t, s, base, map[string]any{"op": "put", "key": k("hello.txt"), "value": "hello again"})
		if put2["version"] == put["version"] || put2["etag"] == put["etag"] {
			t.Fatalf("overwrite: %v", put2)
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("hello.txt")}); g["text"] != "hello again" || len(g["customMetadata"].(map[string]any)) != 0 {
			t.Fatalf("after overwrite: %v", g)
		}
	})

	t.Run("Ranges", func(t *testing.T) {
		r2op(t, s, base, map[string]any{"op": "put", "key": k("digits"), "value": "0123456789"})
		for _, c := range []struct {
			opts map[string]any
			want string
			off  float64
		}{
			{map[string]any{"range": map[string]any{"offset": 2, "length": 3}}, "234", 2},
			{map[string]any{"range": map[string]any{"offset": 7}}, "789", 7},
			{map[string]any{"range": map[string]any{"length": 2}}, "01", 0},
			{map[string]any{"range": map[string]any{"suffix": 4}}, "6789", 6},
			{map[string]any{"range": map[string]any{"offset": 8, "length": 100}}, "89", 8},
			{map[string]any{"rangeHeader": "bytes=1-3"}, "123", 1},
			{map[string]any{"rangeHeader": "bytes=-2"}, "89", 8},
			{map[string]any{"rangeHeader": "bytes=5-"}, "56789", 5},
		} {
			g := r2op(t, s, base, map[string]any{"op": "get", "key": k("digits"), "opts": c.opts})
			rg, _ := g["range"].(map[string]any)
			if g["text"] != c.want || g["size"] != float64(10) || rg["offset"] != c.off || rg["length"] != float64(len(c.want)) {
				t.Errorf("range %v: %v", c.opts, g)
			}
		}
		if e := r2opErr(t, s, base, map[string]any{"op": "get", "key": k("digits"), "opts": map[string]any{"range": map[string]any{"offset": 11}}}); !strings.Contains(e, "range") {
			t.Errorf("unsatisfiable range: %s", e)
		}
	})

	t.Run("OnlyIf", func(t *testing.T) {
		put := r2op(t, s, base, map[string]any{"op": "put", "key": k("cond"), "value": "v1"})
		etag := put["etag"].(string)
		up := put["uploaded"].(float64)
		// Put if absent (If-None-Match: *): refused (null) when present.
		if r := r2op(t, s, base, map[string]any{"op": "put", "key": k("cond"), "value": "v2", "opts": map[string]any{"onlyIf": map[string]any{"etagDoesNotMatch": "*"}}}); r != nil {
			t.Fatalf("put if absent over an existing key: %v", r)
		}
		if r := r2op(t, s, base, map[string]any{"op": "put", "key": k("cond-new"), "value": "n", "opts": map[string]any{"onlyIfHeaders": map[string]string{"if-none-match": "*"}}}); r == nil {
			t.Fatal("put if absent of a new key refused")
		}
		// Compare and swap on the etag.
		if r := r2op(t, s, base, map[string]any{"op": "put", "key": k("cond"), "value": "v2", "opts": map[string]any{"onlyIf": map[string]any{"etagMatches": "nope"}}}); r != nil {
			t.Fatalf("put with a stale etag: %v", r)
		}
		put2 := r2op(t, s, base, map[string]any{"op": "put", "key": k("cond"), "value": "v2", "opts": map[string]any{"onlyIf": map[string]any{"etagMatches": etag}}})
		if put2 == nil {
			t.Fatal("put with the current etag refused")
		}
		etag2 := put2["etag"].(string)
		// Get: a failed condition gives the object without a body.
		g := r2op(t, s, base, map[string]any{"op": "get", "key": k("cond"), "opts": map[string]any{"onlyIf": map[string]any{"etagMatches": etag}}})
		if g == nil || g["hasBody"] != false || g["etag"] != etag2 {
			t.Fatalf("get with a failed etagMatches: %v", g)
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("cond"), "opts": map[string]any{"onlyIf": map[string]any{"etagMatches": etag2}}}); g["text"] != "v2" {
			t.Fatalf("get with etagMatches: %v", g)
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("cond"), "opts": map[string]any{"onlyIfHeaders": map[string]string{"if-none-match": `"` + etag2 + `"`}}}); g["hasBody"] != false {
			t.Fatalf("get with If-None-Match of the current etag: %v", g)
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("cond"), "opts": map[string]any{"onlyIf": map[string]any{"uploadedAfter": up + 60_000}}}); g["hasBody"] != false {
			t.Fatalf("get uploadedAfter the future: %v", g)
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("cond"), "opts": map[string]any{"onlyIf": map[string]any{"uploadedBefore": up + 60_000}}}); g["text"] != "v2" {
			t.Fatalf("get uploadedBefore the future: %v", g)
		}
	})

	t.Run("ListDelete", func(t *testing.T) {
		lp := k("ls/")
		for _, n := range []string{"a", "b/1", "b/2", "c", "d/x/y"} {
			r2op(t, s, base, map[string]any{"op": "put", "key": lp + n, "value": n, "opts": map[string]any{"customMetadata": map[string]any{"n": n}, "httpMetadata": map[string]any{"contentType": "text/x-" + strings.ReplaceAll(n, "/", "-")}}})
		}
		keys := func(r map[string]any) []string {
			var out []string
			for _, o := range r["objects"].([]any) {
				out = append(out, strings.TrimPrefix(o.(map[string]any)["key"].(string), lp))
			}
			return out
		}
		r := r2op(t, s, base, map[string]any{"op": "list", "opts": map[string]any{"prefix": lp}})
		if fmt.Sprint(keys(r)) != "[a b/1 b/2 c d/x/y]" || r["truncated"] != false {
			t.Fatalf("list: %v", r)
		}
		// Without include, no metadata.
		if o := r["objects"].([]any)[0].(map[string]any); len(o["customMetadata"].(map[string]any)) != 0 {
			t.Fatalf("list without include has metadata: %v", o)
		}
		r = r2op(t, s, base, map[string]any{"op": "list", "opts": map[string]any{"prefix": lp, "delimiter": "/"}})
		if fmt.Sprint(keys(r)) != "[a c]" || fmt.Sprint(r["delimitedPrefixes"]) != "["+lp+"b/ "+lp+"d/]" {
			t.Fatalf("list with delimiter: %v", r)
		}
		// Pages of 2, with include.
		var all []string
		cursor := ""
		for i := 0; i < 5; i++ {
			o := map[string]any{"prefix": lp, "limit": 2, "include": []string{"customMetadata", "httpMetadata"}}
			if cursor != "" {
				o["cursor"] = cursor
			}
			r = r2op(t, s, base, map[string]any{"op": "list", "opts": o})
			for _, ob := range r["objects"].([]any) {
				m := ob.(map[string]any)
				n := strings.TrimPrefix(m["key"].(string), lp)
				if m["customMetadata"].(map[string]any)["n"] != n || m["httpMetadata"].(map[string]any)["contentType"] != "text/x-"+strings.ReplaceAll(n, "/", "-") {
					t.Fatalf("list include: %v", m)
				}
			}
			all = append(all, keys(r)...)
			if r["truncated"] != true {
				break
			}
			cursor = r["cursor"].(string)
		}
		if fmt.Sprint(all) != "[a b/1 b/2 c d/x/y]" {
			t.Fatalf("paged list: %v", all)
		}
		r = r2op(t, s, base, map[string]any{"op": "list", "opts": map[string]any{"prefix": lp, "startAfter": lp + "b/1"}})
		if fmt.Sprint(keys(r)) != "[b/2 c d/x/y]" {
			t.Fatalf("startAfter: %v", keys(r))
		}
		// Delete one, then several (one missing).
		r2op(t, s, base, map[string]any{"op": "delete", "key": lp + "a"})
		r2op(t, s, base, map[string]any{"op": "delete", "keys": []string{lp + "b/1", lp + "b/2", lp + "nope"}})
		r = r2op(t, s, base, map[string]any{"op": "list", "opts": map[string]any{"prefix": lp}})
		if fmt.Sprint(keys(r)) != "[c d/x/y]" {
			t.Fatalf("after delete: %v", keys(r))
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": lp + "a"}); g != nil {
			t.Fatalf("get after delete: %v", g)
		}
		// Buckets are separate.
		if r := r2op(t, s, base, map[string]any{"op": "list", "bucket": "OTHER", "opts": map[string]any{"prefix": lp}}); len(r["objects"].([]any)) != 0 {
			t.Fatalf("other bucket sees main's objects: %v", r)
		}
	})

	t.Run("Multipart", func(t *testing.T) {
		const mib = 1 << 20
		r := r2op(t, s, base, map[string]any{"op": "mpu", "key": k("big"), "parts": []int{5 * mib, 5 * mib, 1000}, "opts": map[string]any{"customMetadata": map[string]any{"mp": "yes"}, "httpMetadata": map[string]any{"contentType": "application/x-mp"}}})
		o := r["object"].(map[string]any)
		if o["size"] != float64(10*mib+1000) || !strings.HasSuffix(o["etag"].(string), "-3") {
			t.Fatalf("complete: %v", r)
		}
		// A range across the part boundaries.
		g := r2op(t, s, base, map[string]any{"op": "get", "key": k("big"), "opts": map[string]any{"range": map[string]any{"offset": 5*mib - 2, "length": 5*mib + 4}}})
		txt := g["text"].(string)
		if len(txt) != 5*mib+4 || txt[:2] != "AA" || txt[2:4] != "BB" || txt[len(txt)-2:] != "CC" || g["customMetadata"].(map[string]any)["mp"] != "yes" || g["httpMetadata"].(map[string]any)["contentType"] != "application/x-mp" {
			t.Fatalf("ranged multipart get: len %d head %q tail %q meta %v", len(txt), txt[:4], txt[len(txt)-2:], g["customMetadata"])
		}
		if g := r2op(t, s, base, map[string]any{"op": "get", "key": k("big"), "opts": map[string]any{"range": map[string]any{"suffix": 3}}}); g["text"] != "CCC" {
			t.Fatalf("suffix of multipart: %v", g["text"])
		}
		// Parts smaller than 5 MiB (but the last) are refused; a wrong etag
		// is refused; abort leaves no object.
		if e := r2opErr(t, s, base, map[string]any{"op": "mpu", "key": k("small"), "parts": []int{100, 100}}); !strings.Contains(e, "smaller") {
			t.Fatalf("small parts: %s", e)
		}
		if e := r2opErr(t, s, base, map[string]any{"op": "mpu", "key": k("badetag"), "parts": []int{10}, "badEtag": true}); !strings.Contains(e, "parts could not be found") {
			t.Fatalf("bad part etag: %s", e)
		}
		r2op(t, s, base, map[string]any{"op": "mpu", "key": k("aborted"), "parts": []int{10}, "abort": true})
		if g := r2op(t, s, base, map[string]any{"op": "head", "key": k("aborted")}); g != nil {
			t.Fatalf("aborted upload left an object: %v", g)
		}
	})
}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			r, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	return out
}

// TestR2Streaming proves that object bodies stream: a 1 GiB object is
// copied within a bucket (its get body is the put body) and the copy is
// read back as a stream (hashed in the worker), while the test samples
// hostd's heap (hostd runs in this process) and workerd's RSS. Neither may
// grow by anything like the object's size. (The object is first written
// from a JS generator, which is not measured: its chunks are V8 garbage,
// about 150 MiB of RSS per 512 MiB even without R2.) Request bodies of known length also stream from the
// tailnet into a bucket and back, from the worker and from a facet.
//
// Not parallel: the heap it samples is the whole test process's, which
// the parallel tests would inflate.
func TestR2Streaming(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 1 GiB")
	}
	blobDir := t.TempDir()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.R2 = &r2.FS{Root: blobDir}
	})
	// This test measures memory, not speed: moving 1 GiB through workerd
	// can take well over the suite's 30 s request timeout on a loaded CI
	// runner (issue #75).
	s.hc = s.client.HTTPClient(5 * time.Minute)
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("r2probe"), "r2probe", nil)
	wl := e.WaitActive("r2probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	pid := workloadStatus(t, e, "r2probe").PID

	const size = 1 << 30
	// Write the object from a generator (V8 garbage: not measured).
	code, put := s.getJSON(fmt.Sprintf("%s/gen?key=huge&size=%d", base, size))
	if code != 200 || put["size"] != float64(size) {
		t.Fatalf("gen: %d %v", code, put)
	}
	// Measured: copy it (get body → put) and read the copy back.
	heap0, rss0 := r2Mem(pid)
	stop := make(chan struct{})
	peak := make(chan [2]uint64, 1)
	go func() {
		var ph, pr uint64
		for {
			h, r := r2Mem(pid)
			ph, pr = max(ph, h), max(pr, r)
			select {
			case <-stop:
				peak <- [2]uint64{ph, pr}
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	code, cp := s.getJSON(base + "/copy?from=huge&to=copy")
	if code != 200 || cp["size"] != float64(size) || cp["etag"] != put["etag"] {
		t.Fatalf("copy: %d %v", code, cp)
	}
	code, got := s.getJSON(base + "/sum?key=copy")
	close(stop)
	p := <-peak
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for off := 0; off < size; off += len(buf) {
		for i := range buf {
			buf[i] = byte(((off + i) * 7) % 251)
		}
		h.Write(buf)
	}
	if want := hex.EncodeToString(h.Sum(nil)); code != 200 || got["size"] != float64(size) || got["sha256"] != want {
		t.Fatalf("read back: %d %v, want sha256 %s", code, got, want)
	}
	growHeap, growRSS := int64(p[0])-int64(heap0), int64(p[1])-int64(rss0)
	t.Logf("1 GiB copy + get: hostd heap peak +%d MiB (baseline %d MiB); workerd RSS peak +%d MiB (baseline %d MiB)",
		growHeap>>20, heap0>>20, growRSS>>20, rss0>>20)
	if growHeap > 64<<20 {
		t.Errorf("hostd heap grew %d MiB moving a 1 GiB object: not streaming", growHeap>>20)
	}
	if pid != 0 && rss0 != 0 && growRSS > 384<<20 {
		t.Errorf("workerd RSS grew %d MiB moving a 1 GiB object: not streaming", growRSS>>20)
	}
	if fi, err := os.Stat(filepath.Join(blobDir, filepath.FromSlash(filesUnder(t, blobDir)[0]))); err != nil || fi.Size() != size {
		t.Fatalf("blob on disk: %v %v", fi, err)
	}

	// Request bodies (known length) from the tailnet, worker and facet.
	body := make([]byte, 8<<20+3)
	for i := range body {
		body[i] = byte(i * 13)
	}
	for _, pfx := range []string{"", "/do"} {
		code, _, out := s.do("PUT", base+pfx+"/stream?key=up"+pfx, body)
		if code != 200 {
			t.Fatalf("%s stream put: %d %s", pfx, code, out)
		}
		code, _, back := s.do("GET", base+pfx+"/stream?key=up"+pfx, nil)
		if code != 200 || len(back) != len(body) || string(back) != string(body) {
			t.Fatalf("%s stream get: %d, %d bytes", pfx, code, len(back))
		}
	}
}

// r2Mem is this process's Go heap in use and pid's RSS (0 if unknown).
func r2Mem(pid int) (heap, rss uint64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	heap = ms.HeapInuse
	if pid > 0 {
		if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output(); err == nil {
			if kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				rss = kb << 10
			}
		}
	}
	return heap, rss
}

// TestR2IndexSurvivesMove: the bucket's index is a platform store, so it
// survives losing the host's DO storage (what a move to a new host looks
// like to the bucket): with the S3 backend every object is still there.
// Deleting the bucket (its store, as `tw r2 bucket delete` does) empties
// it and deletes the blobs the index pointed to.
func TestR2IndexSurvivesMove(t *testing.T) {
	t.Parallel()
	fake := r2test.StartFakeS3(t.TempDir())
	defer fake.Close()
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, func(o *hostdtest.Options) {
		o.R2 = fake.Backend("")
		// 5 s, not 1 s: ordinary calls must not trip the stall fence on a loaded
		// runner (#556 gates stateful ingress on heartbeat freshness; #591).
		o.FenceAfter, o.HeartbeatEvery = 5*time.Second, 100*time.Millisecond
	})
	e := s.e
	dep := e.Deploy(hostdtest.Fixture("r2probe"), "r2probe", nil)
	wl := e.WaitActive("r2probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	r2op(t, s, base, map[string]any{"op": "put", "key": "a", "value": "alpha", "opts": map[string]any{"customMetadata": map[string]any{"m": "1"}}})
	r2op(t, s, base, map[string]any{"op": "mpu", "key": "b", "parts": []int{5 << 20, 7}})
	r2op(t, s, base, map[string]any{"op": "put", "key": "gone", "value": "x"})
	r2op(t, s, base, map[string]any{"op": "delete", "key": "gone"})
	// The deleted put's blob goes a moment after the delete returns.
	for deadline := time.Now().Add(10 * time.Second); len(fake.Keys()) != 3 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(fake.Keys()); n != 3 {
		t.Fatalf("blobs after a put, a 2-part upload and a deleted put: %d, want 3 (%v)", n, fake.Keys())
	}

	ws := workloadStatus(t, e, "r2probe")
	e.FM.SetHeartbeatsDown(true)
	deadline := time.Now().Add(20 * time.Second)
	for workloadStatus(t, e, "r2probe").PID != 0 {
		if time.Now().After(deadline) {
			t.Fatal("workload not fenced")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.RemoveAll(ws.DOStorage); err != nil {
		t.Fatal(err)
	}
	e.FM.SetHeartbeatsDown(false)
	wl = e.WaitActive("r2probe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base = "http://" + wl.IP
	if g := r2op(t, s, base, map[string]any{"op": "get", "key": "a"}); g["text"] != "alpha" || g["customMetadata"].(map[string]any)["m"] != "1" {
		t.Fatalf("after losing DO storage, get a: %v", g)
	}
	if g := r2op(t, s, base, map[string]any{"op": "get", "key": "b", "opts": map[string]any{"range": map[string]any{"suffix": 8}}}); g["text"] != "ABBBBBBB" {
		t.Fatalf("after losing DO storage, multipart b: %v", g["text"])
	}
	if r := r2op(t, s, base, map[string]any{"op": "list"}); len(r["objects"].([]any)) != 2 {
		t.Fatalf("after losing DO storage, list: %v", r)
	}

	// Delete the bucket's store: the bucket reads empty, and its blobs go.
	if err := e.Mgmt.Do(context.Background(), "DELETE", "/v1/tenants/"+e.TenantID+"/workloads/r2probe/stores/r2/main", nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := r2op(t, s, base, map[string]any{"op": "list"}); len(r["objects"].([]any)) != 0 {
		t.Fatalf("after bucket delete, list: %v", r)
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(fake.Keys()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("blobs left after the bucket was deleted: %v", fake.Keys())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p := r2op(t, s, base, map[string]any{"op": "put", "key": "new", "value": "n"}); p["size"] != float64(1) {
		t.Fatalf("put after bucket delete: %v", p)
	}
}
