//go:build linux

package attacks

// Pass 2 (live), Linux process tier — the sandbox-escape battery the first
// pass could not run (it worked from macOS). It drives tailworks' real Landlock +
// seccomp profile (internal/procsb, the exact rules, filter and
// no_new_privs a tenant workerd/Node app gets) and attempts every escape the
// brief lists: read/list/write outside the instance dir, raw TCP to the
// internet / cloud metadata / another tenant's loopback port, UDP (DNS),
// AF_UNIX connects to host sockets (tailscaled, a mgmt-shaped socket),
// socketpair abuse, execve of a host binary, and ptrace / process_vm_readv /
// cross-process signals against another process (RT-08 / L2).
//
// Each probe runs in a fresh launcher the profile is applied to; it prints
// "ok" if the operation succeeded and an errno name if the kernel refused
// it. The invariant: only the instance dir, the workerd binary and the
// egress proxy port are reachable; everything else is refused before a
// packet or a syscall effect. Requires Landlock (skips otherwise), so it is
// meaningful only on Linux; run it in the rt2 VM.

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/procsb"
)

func sbSelf(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// sbProbe runs op under the profile and returns "ok" or the errno/err string.
func sbProbe(t *testing.T, prof, op string, args ...string) string {
	t.Helper()
	cmd, err := procsb.ProbeCommand(prof, op, args...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	res := strings.TrimSpace(string(out))
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
			// exit 3 is "refused, errno printed"; anything else is a harness fault.
			t.Fatalf("probe %s %v harness error: %v %s", op, args, err, res)
		}
	}
	return res
}

func TestRT08_LinuxSandboxEscapeBattery(t *testing.T) {
	abi, err := procsb.ABI()
	if err != nil || abi < 1 {
		t.Skipf("RT-08/L2: no Landlock on this kernel (%v); boundary is not available here", err)
	}

	root := t.TempDir()
	own, deny := filepath.Join(root, "own"), filepath.Join(root, "deny")
	for _, d := range []string{own, deny} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A secret outside the instance dir: another tenant's key, say.
	canary := filepath.Join(deny, "victim.key")
	if err := os.WriteFile(canary, []byte("TENANT-B-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "mine"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Another tenant's / a host service's loopback port (denied).
	victim, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer victim.Close()
	go func() {
		for {
			c, e := victim.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	victimPort := uint16(victim.Addr().(*net.TCPAddr).Port)

	// The egress proxy port: the ONE TCP connect a workload may make.
	egress, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer egress.Close()
	go func() {
		for {
			c, e := egress.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	egressPort := uint16(egress.Addr().(*net.TCPAddr).Port)

	// A mgmt-shaped AF_UNIX socket on the host (the RT-03 target): a
	// sandboxed workload must not be able to open any AF_UNIX socket at all.
	usock := filepath.Join(root, "mgmt.sock")
	ul, err := net.Listen("unix", usock)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	go func() {
		for {
			c, e := ul.Accept()
			if e != nil {
				return
			}
			c.Write([]byte("HTTP/1.0 200 OK\r\n\r\nADMIN"))
			c.Close()
		}
	}()

	wd := findWorkerdOrSkip(t)
	prof := filepath.Join(own, "sandbox.json")
	p := &procsb.Profile{
		Launcher:      sbSelf(t),
		Exec:          []string{wd},
		Own:           []string{own},
		ConnectTCP:    []uint16{egressPort}, // only the egress proxy
		NoUnixSockets: true,                 // TCP-listen workloads get no AF_UNIX
	}
	if err := p.Write(prof); err != nil {
		t.Fatal(err)
	}

	// The battery: op, args, and whether the kernel must refuse it.
	type probe struct {
		name       string
		op         string
		args       []string
		mustRefuse bool
	}
	pid1 := strconv.Itoa(os.Getpid()) // this test process: a stand-in for hostd/mgmt/another tenant
	battery := []probe{
		{"read another tenant's key", procsb.ProbeRead, []string{canary}, true},
		{"list the deny dir", procsb.ProbeList, []string{deny}, true},
		{"write outside the instance dir", procsb.ProbeWrite, []string{filepath.Join(deny, "x")}, true},
		{"read own instance file", procsb.ProbeRead, []string{filepath.Join(own, "mine")}, false},
		{"write own instance dir", procsb.ProbeWrite, []string{filepath.Join(own, "created")}, false},
		{"TCP to the internet (1.1.1.1:443)", procsb.ProbeConnectTCP, []string{"1.1.1.1:443"}, true},
		{"TCP to cloud metadata (169.254.169.254:80)", procsb.ProbeConnectTCP, []string{"169.254.169.254:80"}, true},
		{"TCP to another tenant's loopback port", procsb.ProbeConnectTCP, []string{"127.0.0.1:" + strconv.Itoa(int(victimPort))}, true},
		{"TCP to the egress proxy port", procsb.ProbeConnectTCP, []string{"127.0.0.1:" + strconv.Itoa(int(egressPort))}, false},
		{"bind a new TCP port", procsb.ProbeBindTCP, []string{"127.0.0.1:0"}, true},
		{"UDP datagram (DNS 1.1.1.1:53)", procsb.ProbeUDP, []string{"1.1.1.1:53"}, true},
		{"AF_UNIX connect to a host mgmt socket", procsb.ProbeConnectUnix, []string{usock}, true},
		{"AF_UNIX connect to system tailscaled", procsb.ProbeConnectUnix, []string{"/var/run/tailscale/tailscaled.sock"}, true},
		{"socketpair AF_UNIX dgram", procsb.ProbeSocketpair, []string{"dgram"}, true},
		// execve of a SYSTEM binary is allowed by design (systemRO: /bin, /usr,
		// /lib are read+execute so runtimes work); the child inherits Landlock
		// (no_new_privs, so setuid bits are ignored) + seccomp and is confined
		// exactly as the parent. Not an escape — recorded as expected-allowed.
		{"execve a host binary (/bin/cat), child stays confined", procsb.ProbeExec, []string{"/bin/cat"}, false},
		{"ptrace another process", procsb.ProbePtrace, []string{pid1}, true},
		{"process_vm_readv another process", procsb.ProbeVMRead, []string{pid1}, true},
		{"kill() another process", procsb.ProbeSignal, []string{pid1}, true},
		{"tgkill another process", procsb.ProbeTgkill, []string{pid1}, true},
		{"kill(-1) everything", procsb.ProbeSignal, []string{"-1"}, true},
	}

	for _, b := range battery {
		res := sbProbe(t, prof, b.op, b.args...)
		refused := res != "ok"
		if b.mustRefuse && !refused {
			t.Errorf("RT-08/L2 BROKEN: %q was ALLOWED under the tenant sandbox (got %q); a tenant workload escaped the boundary", b.name, res)
		}
		if !b.mustRefuse && refused {
			t.Errorf("RT-08/L2: %q was refused (%q) but must be allowed for a workload to function", b.name, res)
		}
		t.Logf("%-45s op=%-13s -> %s (refused=%v, wantRefuse=%v)", b.name, b.op, res, refused, b.mustRefuse)
	}

	// ABI < 4 cannot restrict TCP connects: record it as the documented
	// cooperative-only case rather than a silent pass.
	if abi < 4 {
		t.Logf("RT-08/L2 NOTE: Landlock ABI %d < 4 does not restrict TCP; egress containment is cooperative here (see docs/node-tier.md)", abi)
	}
}

// TestRT_H342_LandlockDenyLayout (ledger H-342-landlock-deny, issue #342):
// Landlock has no deny rules, so hostd's deny list (Profile.Deny) protects
// the state dir and credential files only while they are outside the trees
// every profile allows reading (/usr, /etc, /opt, …). Before the fix, a
// state dir or credential file placed under such a tree was readable by
// every tenant's workload, and nothing refused the layout.
//
// Part 1 drives the real launcher: a temp dir stands in for /opt (added to
// the profile the way systemRO's entries are: a read-only tree), with the
// state dir's custody key and a tenant's OAuth client file below it and both
// in Deny. ProbeRead on each must be refused: the launcher refuses to apply
// a profile whose deny list it can't honour (before the fix both read "ok").
// Part 2: hostd.New refuses a data dir under a real system tree and a
// -sandbox-deny path under /etc with sandbox_layout_unsafe, before it
// creates anything.
func TestRT_H342_LandlockDenyLayout(t *testing.T) {
	abi, err := procsb.ABI()
	if err != nil || abi < 1 {
		t.Skipf("H-342: no Landlock on this kernel (%v)", err)
	}
	root := t.TempDir()
	optLike := filepath.Join(root, "opt")
	stateDir := filepath.Join(optLike, "tailworks", ".tailworks")
	custody := filepath.Join(stateDir, "mgmt", "custody")
	cred := filepath.Join(optLike, "tailworks", "etc", "oauth.env")
	own := filepath.Join(root, "own")
	for _, d := range []string{custody, filepath.Dir(cred), own} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	custodyKey := filepath.Join(custody, "custody.key")
	for p, v := range map[string]string{custodyKey: "CUSTODY-KEY", cred: "TS_OAUTH_CLIENT_SECRET=tskey-client-SECRET\n"} {
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prof := filepath.Join(own, "sandbox.json")
	p := &procsb.Profile{
		Launcher:      sbSelf(t),
		Exec:          []string{findWorkerdOrSkip(t)},
		ReadOnly:      []string{optLike}, // the /opt stand-in: read and execute below, as systemRO
		Own:           []string{own},
		NoUnixSockets: true,
		Deny:          []string{stateDir, cred}, // what hostd.sandboxDeny would list
	}
	if err := p.Write(prof); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"the custody key": custodyKey, "a tenant's OAuth client file": cred} {
		cmd, err := procsb.ProbeCommand(prof, procsb.ProbeRead, path)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.CombinedOutput()
		res := strings.TrimSpace(string(out))
		if err == nil || res == "ok" {
			t.Errorf("H-342 BROKEN: %s (%s) under a sandbox-readable tree was READ by a sandboxed workload despite being in the deny list", name, path)
			continue
		}
		if !strings.Contains(res, procsb.LayoutErrorCode) {
			t.Errorf("%s: refused, but not by the layout check: %v %q", name, err, res)
		}
		t.Logf("%-28s -> refused: %s", name, firstLineOf(res))
	}

	// Part 2: hostd refuses the layout at start, before creating anything.
	mc, err := mgmtapi.NewClient("unix://" + filepath.Join(root, "no-mgmt.sock"))
	if err != nil {
		t.Fatal(err)
	}
	optState := filepath.Join("/opt", "tw-test-h342-"+strconv.Itoa(os.Getpid()), "state")
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(optState)) }) // only an unfixed hostd running as root creates it
	for name, cfg := range map[string]hostd.Config{
		"state dir under /opt":          {DataDir: optState},
		"credential file under /etc":    {DataDir: filepath.Join(root, "data"), SandboxDeny: []string{"/etc/tailworks/oauth.env"}},
		"run dir under /usr/local":      {DataDir: filepath.Join(root, "data2"), RunDir: "/usr/local/tailworks/run"},
		"state dir symlinked into /opt": {DataDir: linkInto(t, root, optState)},
	} {
		cfg.HostID, cfg.Mgmt, cfg.WorkerdBin, cfg.Sandbox = "h1", mc, sbSelf(t), hostd.SandboxLandlock
		h, err := hostd.New(cfg)
		if h != nil {
			h.Close()
		}
		var le *procsb.LayoutError
		if !errors.As(err, &le) {
			t.Errorf("H-342 BROKEN: hostd.New with %s: got %v, want %s", name, err, procsb.LayoutErrorCode)
			continue
		}
		t.Logf("%-30s -> hostd refused: %v", name, err)
	}
	if _, err := os.Stat(filepath.Dir(optState)); err == nil {
		t.Errorf("hostd created %s before refusing the layout", filepath.Dir(optState))
	}
	// The same config outside the trees is accepted (the check is about
	// location, not about Landlock being on).
	okCfg := hostd.Config{HostID: "h1", Mgmt: mc, WorkerdBin: sbSelf(t), Sandbox: hostd.SandboxLandlock,
		DataDir: filepath.Join(root, "ok-data"), RunDir: filepath.Join(root, "ok-run")}
	h, err := hostd.New(okCfg)
	if err != nil {
		t.Fatalf("hostd.New with a state dir outside the system trees: %v", err)
	}
	h.Close()
}

// linkInto returns a symlink under root that points at target (which need
// not exist): a state dir "outside /opt" that really lives there.
func linkInto(t *testing.T, root, target string) string {
	t.Helper()
	l := filepath.Join(root, "var-lib-tailworks")
	if err := os.Symlink(target, l); err != nil {
		t.Fatal(err)
	}
	return l
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func findWorkerdOrSkip(t *testing.T) string {
	t.Helper()
	for _, c := range []string{
		filepath.Join(os.Getenv("HOME"), ".tailworks/bin/workerd"),
		"/usr/local/bin/workerd",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("workerd"); err == nil {
		return p
	}
	// Any executable works for the non-exec probes; use the test binary
	// itself as the allowed Exec entry so the profile is valid.
	return sbSelf(t)
}
