//go:build e2eplacement

package placement

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/r2/r2test"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// TestR2MoveBetweenHosts is the R2 binding (ADR-036) across real
// processes: tw-mgmt and two tw-hostd processes, both started with
// -r2-backend s3 pointing at one hermetic S3 server (credentials in their
// environment only). An app writes objects on h1, is re-placed onto h2
// (deploy --replace), and reads them back there: the index comes from the
// bucket's journal in mgmt, the bytes from the shared backend. The
// credentials never reach the workload's bundle or config.
func TestR2MoveBetweenHosts(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	dir, err := hostdtest.ShortDir("tw-r2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() || os.Getenv("TW_KEEP") == "" {
			os.RemoveAll(dir)
		}
	})
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	ts := fakeTS(t)
	s3 := r2test.StartFakeS3(t.TempDir())
	t.Cleanup(s3.Close)
	e := &env{t: t, bin: cliflow.BuildBinaries(t), dir: dir, ctl: ctl, sock: filepath.Join(dir, "mgmt.sock"), procs: map[string]*proc{}, workerd: wd}
	t.Cleanup(func() {
		for _, n := range []string{"h1", "h2", "mgmt"} {
			e.stop(n)
		}
	})
	e.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.sock)
	}}}

	keyFile := filepath.Join(dir, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	e.start("mgmt", nil, "-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"), "-rotate-host-token",
		"-state-dir", filepath.Join(dir, "mgmt"), "-tsapi-url", ts.URL, "-reap-interval", "1h")
	for i := 0; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, "token"))
		if _, serr := os.Stat(e.sock); err == nil && len(bytes.TrimSpace(b)) == 64 && serr == nil {
			e.token = string(bytes.TrimSpace(b))
			break
		}
		if i > 100 {
			e.dumpLogs()
			t.Fatal("mgmt did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var tn struct{ TenantID string }
	e.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	creds := []string{"TW_R2_S3_ACCESS_KEY_ID=" + s3.Creds.AccessKeyID, "TW_R2_S3_SECRET_ACCESS_KEY=" + s3.Creds.SecretAccessKey}
	for _, id := range []string{"h1", "h2"} {
		hd := filepath.Join(dir, id)
		e.start(id, creds, "-local-dev", "-mgmt", "unix://"+e.sock, "-mgmt-token-file", filepath.Join(dir, "token"),
			"-host-id", id, "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
			"-control-url", ctl.URL, "-ephemeral", "-heartbeat", "500ms",
			"-sandbox", "none", "-workerd", wd, "-hostname-prefix", id+"-", "-log-level", "debug",
			"-r2-backend", "s3", "-r2-s3-endpoint", s3.URL, "-r2-s3-bucket", s3.Bucket, "-r2-s3-prefix", "shared")
	}
	for i := 0; ; i++ {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		e.must("GET", "/v1/hosts", nil, &pg)
		live := 0
		for _, h := range pg.Items {
			if h.Status == "live" {
				live++
			}
		}
		if live == 2 {
			break
		}
		if i > 200 {
			e.dumpLogs()
			t.Fatalf("hosts not live: %+v", pg.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}

	files, err := hostdtest.Build(hostdtest.Fixture("r2probe"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	deploy := func(body map[string]any) deployment {
		t.Helper()
		body["tenantID"], body["bundle"], body["workload"] = tn.TenantID, bundle, "r2probe"
		var d deployment
		if st, b := e.do("POST", "/v1/deployments", body, &d); st != 202 {
			e.dumpLogs()
			t.Fatalf("deploy %v: %d %s", body["host"], st, b)
		}
		return e.waitActive(d.DeploymentID, 90*time.Second)
	}

	client := &tsnet.Server{Hostname: "r2-client", ControlURL: ctl.URL, AuthKey: nettest.DummyKey, Ephemeral: true, Store: new(mem.Store),
		Dir: filepath.Join(dir, "client"), Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: client.Dial}}
	op := func(host string, a map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(a)
		for i := 0; ; i++ {
			resp, err := hc.Post("http://"+host+"/op", "application/json", bytes.NewReader(b))
			if err == nil {
				out, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					e.dumpLogs()
					t.Fatalf("op %v on %s: %d %s", a["op"], host, resp.StatusCode, out)
				}
				var m map[string]any
				json.Unmarshal(out, &m)
				return m
			}
			if i > 50 {
				e.dumpLogs()
				t.Fatalf("op %v on %s: %v", a["op"], host, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// On h1: a plain object, metadata, a multipart object.
	d1 := deploy(map[string]any{"host": "h1"})
	put := op(d1.Endpoint.MagicDNS, map[string]any{"op": "put", "key": "notes/a.txt", "value": "written on h1",
		"opts": map[string]any{"customMetadata": map[string]any{"host": "h1"}, "httpMetadata": map[string]any{"contentType": "text/plain"}}})
	op(d1.Endpoint.MagicDNS, map[string]any{"op": "mpu", "key": "big.bin", "parts": []int{5 << 20, 100}})

	// Move to h2 and read it all back there.
	d2 := deploy(map[string]any{"host": "h2", "replace": true, "forceTakeover": true}) // Box (a DO) starts empty on h2; the bucket does not
	if d2.Host != "h2" {
		t.Fatalf("replace landed on %s", d2.Host)
	}
	g := op(d2.Endpoint.MagicDNS, map[string]any{"op": "get", "key": "notes/a.txt"})
	if g["text"] != "written on h1" || g["etag"] != put["etag"] || g["customMetadata"].(map[string]any)["host"] != "h1" {
		t.Fatalf("on h2, get: %v", g)
	}
	g = op(d2.Endpoint.MagicDNS, map[string]any{"op": "get", "key": "big.bin", "opts": map[string]any{"range": map[string]any{"offset": 5<<20 - 1, "length": 2}}})
	if g["text"] != "AB" || g["size"] != float64(5<<20+100) {
		t.Fatalf("on h2, ranged multipart get: %v", g)
	}
	ls := op(d2.Endpoint.MagicDNS, map[string]any{"op": "list"})
	if n := len(ls["objects"].([]any)); n != 2 {
		t.Fatalf("on h2, list: %v", ls)
	}
	// h2 writes too, and the conditional put sees h1's object.
	if r := op(d2.Endpoint.MagicDNS, map[string]any{"op": "put", "key": "notes/a.txt", "value": "x", "opts": map[string]any{"onlyIf": map[string]any{"etagDoesNotMatch": "*"}}}); r != nil {
		t.Fatalf("on h2, put-if-absent over h1's object: %v", r)
	}
	op(d2.Endpoint.MagicDNS, map[string]any{"op": "delete", "key": "big.bin"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		ks := s3.Keys()
		if len(ks) == 1 && strings.HasPrefix(ks[0], "shared/t/"+tn.TenantID+"/w/r2probe/b/main/") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("S3 keys after deleting the multipart object: %v", ks)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s3.Denied.Load() != 0 {
		t.Fatalf("S3 refused %d signatures", s3.Denied.Load())
	}
	// The secret stayed in the hosts' environment: not in any instance
	// config (what workerd and the workload see) nor in the logs.
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(p, "/client/") {
			return nil
		}
		if fi, _ := d.Info(); fi != nil && fi.Size() < 64<<20 {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(s3.Creds.SecretAccessKey)) {
				t.Errorf("the S3 secret is in %s", p)
			}
		}
		return nil
	})
	t.Logf("R2 across hosts: objects written on h1 read on h2 after --replace (%d S3 puts, %d gets)", s3.Puts.Load(), s3.Gets.Load())
}
