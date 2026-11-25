//go:build e2eheadscale

package headscale_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/headscale"
)

// DefaultImage is the Headscale release the profile is gated against
// (ADR-015; RFC §12 "CI gates the profile against a pinned Headscale
// release"). Bump it with `make e2e-headscale HEADSCALE_IMAGE=…`.
const DefaultImage = "headscale/headscale:v0.29.7"

// BaseDomain is the test server's dns.base_domain (tailworks' "tailnet").
const BaseDomain = "hs.tw.test"

// server is a Headscale server for the suite: either an external one
// (TW_HEADSCALE_URL + TW_HEADSCALE_API_KEY_FILE, policy.mode database)
// or a fresh container of DefaultImage (needs docker).
type server struct {
	URL     string
	KeyFile string // HEADSCALE_API_KEY=… (0600)
	Client  *headscale.Client
	Domain  string
	logs    func() string
}

func startServer(t *testing.T) *server {
	t.Helper()
	image := os.Getenv("HEADSCALE_IMAGE")
	if image == "" {
		image = DefaultImage
	}
	return startServerImage(t, image)
}

// OldImage is the newest Headscale release the profile does NOT support
// (no grants): tailworks must refuse it up front.
const OldImage = "headscale/headscale:v0.28.0"

func startServerImage(t *testing.T, image string) *server {
	t.Helper()
	if u := os.Getenv("TW_HEADSCALE_URL"); u != "" {
		kf := os.Getenv("TW_HEADSCALE_API_KEY_FILE")
		b, err := os.ReadFile(kf)
		if err != nil {
			t.Fatalf("TW_HEADSCALE_API_KEY_FILE: %v", err)
		}
		key := strings.TrimPrefix(strings.TrimSpace(string(b)), headscale.APIKeyEnv+"=")
		domain := os.Getenv("TW_HEADSCALE_BASE_DOMAIN")
		return &server{URL: u, KeyFile: kf, Domain: domain, logs: func() string { return "" },
			Client: &headscale.Client{BaseURL: u, APIKey: key, BaseDomain: domain}}
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("e2e-headscale: SKIPPED (no docker, and TW_HEADSCALE_URL is not set)")
	}
	if out, err := exec.Command(docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("e2e-headscale: SKIPPED (docker daemon unavailable: %s)", strings.TrimSpace(string(out)))
	}
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	// Short path: Docker Desktop shares /tmp, and the config is mounted.
	dir, err := os.MkdirTemp("/tmp", "tw-hs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.MkdirAll(filepath.Join(dir, "lib"), 0o777)
	cfg := fmt.Sprintf(`server_url: %s
listen_addr: 0.0.0.0:8080
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false
noise:
  private_key_path: /var/lib/headscale/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential
derp:
  server:
    enabled: false
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: false
  update_frequency: 24h
disable_check_updates: true
node:
  ephemeral:
    inactivity_timeout: 30m
database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
log:
  level: info
policy:
  mode: database
dns:
  magic_dns: true
  base_domain: %s
  override_local_dns: false
  nameservers:
    global: []
unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"
`, url, BaseDomain)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("tw-e2e-hs-%d", port)
	run := exec.Command(docker, "run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
		"-v", filepath.Join(dir, "config.yaml")+":/etc/headscale/config.yaml:ro",
		"-v", filepath.Join(dir, "lib")+":/var/lib/headscale",
		image, "serve")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v\n%s", image, err, out)
	}
	logs := func() string {
		out, _ := exec.Command(docker, "logs", "--tail", "60", name).CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("headscale logs:\n%s", logs())
		}
		exec.Command(docker, "rm", "-f", name).Run()
	})
	dl := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(url + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(dl) {
			t.Fatalf("headscale did not become healthy at %s\n%s", url, logs())
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, err := exec.Command(docker, "exec", name, "headscale", "apikeys", "create", "--expiration", "2h").Output()
	if err != nil {
		t.Fatalf("headscale apikeys create: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	key := strings.TrimSpace(lines[len(lines)-1])
	kf := filepath.Join(dir, "headscale.env")
	if err := os.WriteFile(kf, []byte(headscale.APIKeyEnv+"="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("headscale %s at %s (base domain %s)", image, url, BaseDomain)
	return &server{URL: url, KeyFile: kf, Domain: BaseDomain, logs: logs,
		Client: &headscale.Client{BaseURL: url, APIKey: key, BaseDomain: BaseDomain}}
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
