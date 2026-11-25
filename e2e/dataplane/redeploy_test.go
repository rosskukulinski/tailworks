//go:build e2edataplane

package dataplane

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestRedeployAfterUndeploy: a stateful workload that once ran on a host,
// moved away, was undeployed and is deployed again onto that host comes up
// (its entry node gets ready). Found by the routing loop (#422,
// revoke-binding): the host rejoined the node from its saved state, whose
// object mgmt had purged at the undeploy, and the endpoint stayed pending.
func TestRedeployAfterUndeploy(t *testing.T) {
	if os.Getenv("TW_DP_KNOWN_FAILURES") == "" {
		t.Skip("known failure, #593 (TW_DP_KNOWN_FAILURES=1 runs it)")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	work, err := hostdtest.ShortDir("tw-rd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	rdir := t.TempDir()
	pids, err := os.Create(filepath.Join(rdir, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	c := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}
	keyFile := filepath.Join(work, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	c.startMgmt(true)
	var tn struct{ TenantID string }
	c.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	c.tenantID = tn.TenantID
	for _, id := range c.hosts {
		c.proxies[id] = startProxy(t, filepath.Join(work, id+"-mgmt.sock"), c.sock)
		c.startHost(id)
	}
	c.waitHostsLive(c.hosts...)
	app, _ := filepath.Abs(filepath.Join("testdata", "routing", "b"))
	files, err := hostdtest.Build(app, map[string]string{"VER": "b1"})
	if err != nil {
		t.Fatal(err)
	}
	rc := &rcluster{cluster: c, tgs: map[string]*target{"rt-b": {seen: map[string]string{}}}}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	step := func(what, host string, live bool) deployment {
		t.Helper()
		d, how, err := rc.moveW("rt-b", bundle, host, live, 60*time.Second)
		if err != nil {
			c.dumpLogs()
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: on %s at epoch %d (%s)", what, d.Host, d.Epoch, how)
		return d
	}
	step("deploy", "h1", false)
	step("move to h2", "h2", true)
	d := step("move back to h1", "h1", true)
	if st, b, err := c.try("DELETE", "/v1/deployments/"+d.DeploymentID+"?reason=test", nil, nil); err != nil || st/100 != 2 {
		t.Fatalf("undeploy: %d %s %v", st, b, err)
	}
	// h2 still has the node's saved state from its time there.
	step("deploy again, onto h2", "h2", false)
}
