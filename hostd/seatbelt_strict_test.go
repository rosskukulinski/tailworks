package hostd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// The strict profile's shape (issue #121): global denies first, each
// allow after the deny it narrows (Seatbelt: the last match wins).
func TestSeatbeltStrictProfileShape(t *testing.T) {
	t.Parallel()
	dir := realPath(t.TempDir())
	p := seatbeltProfile([]string{dir + "/run/i1"}, []string{"/opt/workerd"}, nil, []string{dir + "/data"},
		[]uint16{4001}, []uint16{4001, 4002})
	lines := strings.Split(p, "\n")
	at := func(s string) int {
		for i, l := range lines {
			if l == s {
				return i
			}
		}
		t.Fatalf("profile lacks %q:\n%s", s, p)
		return -1
	}
	order := [][2]string{
		{`(deny file-write*)`, `(allow file-read* file-write* (subpath "` + dir + `/run/i1"))`},
		{`(deny file-write*)`, `(allow file-write* (literal "/dev/null"))`},
		{`(deny file-read*)`, `(allow file-read* (subpath "/usr"))`},
		{`(allow file-read* (subpath "/opt"))`, `(deny file-read* file-write* (subpath "` + dir + `/data"))`},
		{`(deny file-read*)`, `(allow file-read-metadata (literal "/etc"))`},
		{`(deny network*)`, `(allow network-outbound (remote tcp4 "localhost:4002"))`},
		{`(deny network*)`, `(allow network-bind network-inbound (local tcp4 "localhost:4001"))`},
		{`(deny network*)`, `(allow network-outbound (remote unix-socket (subpath "` + dir + `/run/i1")))`},
		{`(deny signal)`, `(allow signal (target same-sandbox))`},
		{`(deny process-exec*)`, `(allow process-exec (literal "/opt/workerd"))`},
		{`(deny file-read* file-write* (subpath "` + dir + `/data"))`, `(allow file-read* (literal "/opt/workerd"))`},
	}
	for _, o := range order {
		if at(o[1]) < at(o[0]) {
			t.Errorf("%q must follow %q", o[1], o[0])
		}
	}
	if strings.Contains(p, `tcp "`) || strings.Contains(p, `tcp4 "*`) || strings.Contains(p, "localhost:4003") ||
		strings.Contains(p, `(subpath "/Users")`) || strings.Contains(p, `(subpath "/private/tmp")`) {
		t.Fatalf("unexpected TCP allow:\n%s", p)
	}
}

// Under the strict profile, the probes that succeeded under the old
// (allow default) profile fail: a TCP connect to a port it wasn't given, a
// signal to another process, a write outside its dirs, a TCP bind of a
// port it wasn't given, an exec of a binary outside the allowed dirs, a
// read outside the read allow list (/tmp, the user's temp dir) and a
// connect to its allowed port on [::1] (issue #175). What a workload needs
// still works: its own dir, its own unix sockets, its TCP ports, signalling
// itself and its children, /etc, /bin/sh, workerd and node.
func TestSeatbeltStrictProbes(t *testing.T) {
	t.Parallel()
	if _, err := ResolveSandbox(SandboxSeatbelt); err != nil {
		t.Skip(err)
	}
	tmp := realPath(t.TempDir())
	own, deny, other := filepath.Join(tmp, "own"), filepath.Join(tmp, "deny"), filepath.Join(tmp, "other")
	for _, d := range []string{own, deny, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	listen := func() (net.Listener, uint16) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		return ln, uint16(ln.Addr().(*net.TCPAddr).Port)
	}
	_, allowed := listen()   // the egress proxy
	_, forbidden := listen() // another tenant's port, hostd's, …
	// The allowed port on another loopback address (issue #175): Seatbelt
	// matches TCP by "localhost:<port>", so without tcp4 a workload reaches
	// whatever binds [::1]:<allowed> too.
	var v6 []string
	if ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(int(allowed)))); err == nil {
		t.Cleanup(func() { ln6.Close() })
		go func() {
			for {
				c, err := ln6.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		v6 = []string{"/usr/bin/nc", "-z", "-w", "3", "::1", strconv.Itoa(int(allowed))}
	} else {
		t.Logf("no [::1]:%d listener (%v): skipping the IPv6 loopback probe", allowed, err)
	}
	bindOK, bindNo := freePort(t), freePort(t)
	// A unix socket outside its dirs (say, tailscaled's); short path.
	sdir, err := os.MkdirTemp("/tmp", "twsb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sdir)
	sock := filepath.Join(realPath(sdir), "s.sock")
	uln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	go func() {
		for {
			c, err := uln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	echo := filepath.Join(other, "echo")
	ownEcho := filepath.Join(own, "echo")
	for _, p := range []string{echo, ownEcho} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(p).CombinedOutput(); err != nil {
			t.Fatalf("%s unsandboxed: %v %s", p, err, out)
		}
	}
	readOnly := []string{}
	wd, wdErr := nettest.WorkerdBinary()
	if wdErr == nil {
		readOnly = append(readOnly, wd)
	}
	nodeBin, nodeErr := exec.LookPath("node")
	var trees []string
	if nodeErr == nil {
		readOnly = append(readOnly, nodeBin)
		trees = nodePrefix(nodeBin)
	}
	home, _ := os.UserHomeDir()
	prof := filepath.Join(own, "sandbox.sb")
	text := seatbeltProfile([]string{own}, readOnly, trees, []string{deny, home}, []uint16{bindOK}, []uint16{allowed, bindOK})
	if err := os.WriteFile(prof, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, sandboxExec, append([]string{"-f", prof}, args...)...)
		cmd.Dir = own // as hostd runs workerd and node
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// The probes work unsandboxed, so a failure below is the profile's.
	victim := exec.Command("/bin/sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer victim.Process.Kill()
	tmpFile := fmt.Sprintf("/tmp/tw-seatbelt-probe-%d", os.Getpid())
	defer os.Remove(tmpFile)
	// Readable to the user, outside the read allow list and the deny list
	// (another user's world-readable file, a shared dir, …): /etc/hosts-like.
	readTmp := fmt.Sprintf("/tmp/tw-seatbelt-read-%d", os.Getpid())
	readOther := filepath.Join(other, "hosts")
	for _, p := range []string{readTmp, readOther} {
		if err := os.WriteFile(p, []byte("127.0.0.1 secret.internal\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(p)
	}
	nc := func(port uint16) []string {
		return []string{"/usr/bin/nc", "-z", "-w", "3", "127.0.0.1", strconv.Itoa(int(port))}
	}
	bind := func(port uint16) []string {
		return []string{"/usr/bin/perl", "-MIO::Socket::INET", "-e",
			`IO::Socket::INET->new(LocalAddr => "127.0.0.1", LocalPort => $ARGV[0], Listen => 1, ReuseAddr => 1) or die "bind: $!\n"`,
			strconv.Itoa(int(port))}
	}
	denied := map[string][]string{
		"tcp_connect_other_port": nc(forbidden),
		"tcp_connect_internet":   {"/usr/bin/nc", "-z", "-w", "3", "1.1.1.1", "443"},
		"tcp_bind_other_port":    bind(bindNo),
		"signal_other_process":   {"/bin/kill", "-0", strconv.Itoa(victim.Process.Pid)},
		"signal_hostd":           {"/bin/kill", "-0", strconv.Itoa(os.Getpid())},
		"write_outside_own":      {"/bin/sh", "-c", "echo x > " + filepath.Join(other, "f")},
		"write_tmp":              {"/bin/sh", "-c", "echo x > " + tmpFile},
		"read_denied":            {"/bin/ls", deny},
		"read_tmp":               {"/bin/cat", readTmp},
		"read_user_temp_dir":     {"/bin/cat", readOther},
		"list_user_temp_dir":     {"/bin/ls", other},
		"exec_outside_allowed":   {echo, "hi"},
		"unix_connect_not_own":   {"/usr/bin/perl", "-MIO::Socket::UNIX", "-e", `IO::Socket::UNIX->new(Peer => $ARGV[0]) or die "$!\n"`, sock},
	}
	if v6 != nil {
		denied["tcp_connect_given_port_ipv6"] = v6
	}
	for name, args := range denied {
		if name != "tcp_connect_internet" { // offline CI hosts
			if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
				t.Fatalf("%s fails unsandboxed too, so it proves nothing: %v %s", name, err, out)
			}
			os.Remove(tmpFile)
			os.Remove(filepath.Join(other, "f"))
		}
		if out, err := run(args...); err == nil {
			t.Errorf("%s: allowed under the strict profile (%v): %s", name, args, out)
		}
	}
	allowedProbes := map[string][]string{
		"tcp_connect_given_port": nc(allowed),
		"tcp_bind_own_port":      bind(bindOK),
		"signal_self":            {"/bin/sh", "-c", "kill -0 $$"},
		"signal_child":           {"/bin/sh", "-c", "/bin/sleep 30 & kill $!"},
		"write_own":              {"/bin/sh", "-c", "echo x > " + filepath.Join(own, "f") + " && cat " + filepath.Join(own, "f")},
		"write_dev_null":         {"/bin/sh", "-c", "echo x > /dev/null"},
		"read_etc":               {"/bin/cat", "/etc/hosts", "/etc/ssl/cert.pem", "/etc/localtime"},
		"read_dev_urandom":       {"/bin/sh", "-c", "head -c 16 /dev/urandom | /usr/bin/od -An -tx1"},
		"exec_own":               {ownEcho, "hi"},
		"unix_socket_own":        {"/usr/bin/perl", "-MIO::Socket::UNIX", "-e", `IO::Socket::UNIX->new(Local => $ARGV[0], Listen => 1) or die "$!\n"`, filepath.Join(own, "s.sock")},
	}
	if wdErr == nil {
		allowedProbes["workerd"] = []string{wd, "--version"}
	}
	if nodeErr == nil {
		// node listens on its own port and connects to the egress proxy.
		// Under the read allow list it still loads its builtins, ICU and
		// time zones, OpenSSL's config and CA roots, and reads /etc.
		allowedProbes["node"] = []string{nodeBin, "-e", fmt.Sprintf(`const net = require("net");
require("fs").readFileSync("/etc/hosts");
new Intl.DateTimeFormat("de-DE", { timeZone: "America/New_York" }).format(new Date());
require("tls").rootCertificates.length || process.exit(2);
require("crypto").randomUUID(); require("zlib").gzipSync("x"); require("os").cpus();
const srv = net.createServer((c) => c.end()).listen(%d, "127.0.0.1", () => {
  const c = net.connect(%d, "127.0.0.1", () => { c.end(); srv.close(); });
  c.on("error", (e) => { console.error(e); process.exit(1); });
});
srv.on("error", (e) => { console.error(e); process.exit(1); });`, bindOK, allowed)}
	}
	t.Logf("workerd: %v; node: %v", wd, nodeBin)
	for name, args := range allowedProbes {
		if out, err := run(args...); err != nil {
			t.Errorf("%s: denied under the strict profile (%v): %v %s", name, args, err, out)
		}
	}
	if _, err := os.Stat(tmpFile); err == nil {
		t.Errorf("%s was written from the sandbox", tmpFile)
	}
	if victim.ProcessState != nil {
		t.Error("the victim process exited")
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	p, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	return uint16(p)
}
