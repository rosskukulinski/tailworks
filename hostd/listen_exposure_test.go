package hostd_test

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// TestRT_HLanBind_NodeListenOnlyViaIngress (ledger H-lan-bind): a sandboxed
// Node app is reachable only through tailworks' ingress, never on a
// non-loopback address of the host. Two apps are checked: one that ignores
// HOST and calls listen(PORT) (Node then binds every interface), and raw
// libuv listeners from inside the app (below any JS patching, as hostile
// code would) on 0.0.0.0, [::] and the host's LAN address, on its own port
// and on others, and with no bind at all. Before the fix the Seatbelt and
// Landlock bind rules matched the port only, so listen(PORT) and a raw
// 0.0.0.0:<PORT> listener were reachable on the LAN address.
func TestRT_HLanBind_NodeListenOnlyViaIngress(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := os.Stat(hostdtest.NodeDeps()); err != nil {
		t.Skip("node tier deps not installed (make test-node)")
	}
	sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto)
	if sb == hostd.SandboxNone {
		t.Skip("no process sandbox on this host")
	}
	lan := nonLoopbackIPv4()
	if lan == nil {
		t.Skip("no non-loopback IPv4 address on this host")
	}
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
	}, func(o *hostdtest.Options) { o.Sandbox = hostd.SandboxAuto })
	e := s.e
	dep := e.DeployNode(hostdtest.Fixture("nodeprobe"), "nodeprobe", map[string]string{"PROBE_LISTEN_ALL": "1"})
	wl := e.WaitActive("nodeprobe", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	if code, _, b := s.do("GET", base+"/", nil); code != 200 {
		t.Fatalf("ingress to an app that listens on every interface: %d %s", code, b)
	}
	var port int
	for _, w := range e.Host.Status().Workloads {
		if w.Workload == "nodeprobe" && w.Node != nil {
			if w.Sandbox != sb {
				t.Fatalf("node app sandbox %q, want %q", w.Sandbox, sb)
			}
			port, _ = strconv.Atoi(strings.TrimPrefix(w.Node.Addr, "127.0.0.1:"))
		}
	}
	if port == 0 {
		t.Fatal("no node app port in status")
	}
	reachable := func(ip net.IP, p int) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(p)), 2*time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	// The probes work unsandboxed: a listener on 0.0.0.0 is reachable on lan.
	if ln, err := net.Listen("tcp4", "0.0.0.0:0"); err == nil {
		ok := reachable(lan, ln.Addr().(*net.TCPAddr).Port)
		ln.Close()
		if !ok {
			t.Skipf("a 0.0.0.0 listener isn't reachable on %s even unsandboxed (firewall?)", lan)
		}
	}
	if reachable(lan, port) {
		t.Errorf("the app's listen(PORT) is reachable on %s:%d, bypassing ingress", lan, port)
	}
	other := freeTCPPort(t)
	for name, q := range map[string]string{
		"own_port_any4":  "host=0.0.0.0&port=" + strconv.Itoa(port),
		"own_port_any6":  "host=::&port=" + strconv.Itoa(port),
		"own_port_lan":   "host=" + lan.String() + "&port=" + strconv.Itoa(port),
		"other_port_any": "host=0.0.0.0&port=" + strconv.Itoa(other),
		"ephemeral_any":  "host=0.0.0.0&port=0",
		"unbound":        "",
	} {
		code, _, b := s.do("GET", base+"/listen?"+q, nil)
		if code != 200 {
			t.Fatalf("%s: /listen?%s: %d %s", name, q, code, b)
		}
		var r struct {
			OK   bool
			Code string
			Port int
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if !r.OK {
			t.Logf("%s: refused (%s)", name, r.Code)
			continue
		}
		if r.Port != 0 && reachable(lan, r.Port) {
			t.Errorf("%s: a raw listener in the sandboxed app is reachable on %s:%d", name, lan, r.Port)
		} else {
			t.Logf("%s: listen returned ok (port %d) but nothing is reachable on %s", name, r.Port, lan)
		}
	}
	// What the app needs still works: ingress, and its own port on loopback
	// (self-calls such as the Workflow local world's queue).
	if code, _, b := s.do("GET", base+"/net?host=127.0.0.1&port="+strconv.Itoa(port), nil); code != 200 || !strings.Contains(string(b), `"ok":true`) {
		t.Errorf("app can't reach its own port on 127.0.0.1: %d %s", code, b)
	}
	if code, _, b := s.do("GET", base+"/?again="+url.QueryEscape("1"), nil); code != 200 {
		t.Errorf("ingress after the probes: %d %s", code, b)
	}
}

// nonLoopbackIPv4 is an up interface's IPv4 address that isn't loopback.
func nonLoopbackIPv4() net.IP {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				return n.IP.To4()
			}
		}
	}
	return nil
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
