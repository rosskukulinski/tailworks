//go:build linux

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// kstateProbe runs inside a guest (as root, via guestd's debug "run") and
// prints the kernel state that a snapshot clones and the post-load reseed
// cannot reach (ADR-030 (d), ledger H24):
//   - isn / isnClock: the ISN of a loopback connection on a fixed 4-tuple
//     and (CLOCK_REALTIME ns >> 6) just before connect. Linux's ISN is
//     siphash(4-tuple, net_secret) + (realtime ns >> 6), so isn - isnClock
//     is the keyed hash alone: equal across guests iff net_secret is.
//   - tsval / tsMono: TCP_TIMESTAMP on that socket (monotonic ms + a
//     per-flow offset keyed by ts_secret) and CLOCK_MONOTONIC ms.
//   - eport: the first ephemeral port of an unbound connect to a fixed
//     destination (net_secret + the port-perturbation table).
//   - text: the kernel's _text address (KASLR).
//   - tfoKey: the TCP Fast Open server key.
const kstateProbe = `package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	tcpRepair      = 19
	tcpRepairQueue = 20
	tcpQueueSeq    = 21
	tcpTimestamp   = 24
	tcpSendQueue   = 2
)

type out struct {
	Text     string ` + "`json:\"text\"`" + `
	ISN      uint32 ` + "`json:\"isn\"`" + `
	ISNClock uint32 ` + "`json:\"isnClock\"`" + `
	TSVal    uint32 ` + "`json:\"tsval\"`" + `
	TSMono   uint32 ` + "`json:\"tsMono\"`" + `
	EPort    int    ` + "`json:\"eport\"`" + `
	TFOKey   string ` + "`json:\"tfoKey\"`" + `
	Err      string ` + "`json:\"err,omitempty\"`" + `
}

func lo(port int) *syscall.SockaddrInet4 {
	return &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
}

func sock() int {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		fail(err)
	}
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	return fd
}

var o out

func fail(err error) {
	o.Err = err.Error()
	json.NewEncoder(os.Stdout).Encode(o)
	os.Exit(1)
}

func monoMS() uint32 {
	var ts syscall.Timespec
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	return uint32(ts.Nano() / 1e6)
}

func main() {
	ln := sock()
	if err := syscall.Bind(ln, lo(41000)); err != nil {
		fail(err)
	}
	if err := syscall.Listen(ln, 16); err != nil {
		fail(err)
	}
	c := sock()
	if err := syscall.Bind(c, lo(41001)); err != nil {
		fail(err)
	}
	t0 := time.Now().UnixNano()
	if err := syscall.Connect(c, lo(41000)); err != nil {
		fail(err)
	}
	ts, err := syscall.GetsockoptInt(c, syscall.IPPROTO_TCP, tcpTimestamp)
	if err != nil {
		fail(err)
	}
	o.TSVal, o.TSMono = uint32(ts), monoMS()
	o.ISNClock = uint32(uint64(t0) >> 6)
	// Repair mode reads write_seq (ISN+1 after the SYN) and closes the
	// socket without a FIN, so the fixed 4-tuple is free for a rerun.
	if err := syscall.SetsockoptInt(c, syscall.IPPROTO_TCP, tcpRepair, 1); err != nil {
		fail(err)
	}
	if err := syscall.SetsockoptInt(c, syscall.IPPROTO_TCP, tcpRepairQueue, tcpSendQueue); err != nil {
		fail(err)
	}
	seq, err := syscall.GetsockoptInt(c, syscall.IPPROTO_TCP, tcpQueueSeq)
	if err != nil {
		fail(err)
	}
	o.ISN = uint32(seq) - 1
	syscall.Close(c)
	e := sock()
	if err := syscall.Connect(e, lo(41000)); err != nil {
		fail(err)
	}
	if sa, err := syscall.Getsockname(e); err == nil {
		o.EPort = sa.(*syscall.SockaddrInet4).Port
	}
	syscall.SetsockoptInt(e, syscall.IPPROTO_TCP, tcpRepair, 1)
	syscall.Close(e)
	syscall.Close(ln)
	if f, err := os.Open("/proc/kallsyms"); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			if fs := strings.Fields(s.Text()); len(fs) >= 3 && fs[2] == "_text" {
				o.Text = fs[0]
				break
			}
		}
		f.Close()
	}
	k, _ := os.ReadFile("/proc/sys/net/ipv4/tcp_fastopen_key")
	o.TFOKey = strings.TrimSpace(string(k))
	json.NewEncoder(os.Stdout).Encode(o)
}
`

type kstate struct {
	Text     string `json:"text"`
	ISN      uint32 `json:"isn"`
	ISNClock uint32 `json:"isnClock"`
	TSVal    uint32 `json:"tsval"`
	TSMono   uint32 `json:"tsMono"`
	EPort    int    `json:"eport"`
	TFOKey   string `json:"tfoKey"`
	Err      string `json:"err,omitempty"`
}

// isnKey is the ISN with the clock term removed: siphash(4-tuple,
// net_secret), up to the few µs between the probe's clock read and the
// kernel's (a few hundred units of 64 ns).
func (k kstate) isnKey() uint32 { return k.ISN - k.ISNClock }

// tsKey is the TCP timestamp offset (keyed by ts_secret), ±1 ms.
func (k kstate) tsKey() uint32 { return k.TSVal - k.TSMono }

func near(a, b, tol uint32) bool {
	d := a - b
	return d <= tol || -d <= tol
}

// sameNetSecret: the two guests' ISN keys agree within 4 ms of clock
// error (2^16 × 64 ns). Two independent keys collide by chance with
// probability 2^-15.
func sameNetSecret(a, b kstate) bool { return near(a.isnKey(), b.isnKey(), 1<<16) }

func sameTSSecret(a, b kstate) bool { return near(a.tsKey(), b.tsKey(), 2) }

// buildKstateProbe compiles kstateProbe for the guest (this host's arch).
func buildKstateProbe(t *testing.T) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH: can't build the in-guest probe")
	}
	d := t.TempDir()
	src := filepath.Join(d, "main.go")
	os.WriteFile(src, []byte(kstateProbe), 0o600)
	bin := filepath.Join(d, "kstate")
	cmd := exec.Command(gobin, "build", "-o", bin, src)
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the in-guest probe: %v\n%s", err, out)
	}
	return bin
}

func runKstate(ctx context.Context, m sandbox.VM) (kstate, error) {
	var k kstate
	r, err := m.Op(ctx, proto.Msg{Op: "run", Argv: []string{proto.AppDir + "/kstate"}})
	if err != nil {
		return k, err
	}
	if err := json.Unmarshal([]byte(r.Text), &k); err != nil {
		return k, fmt.Errorf("probe output %q (exit %d, %s): %v", r.Text, r.ExitCode, r.Error, err)
	}
	if k.Err != "" || r.ExitCode != 0 {
		return k, fmt.Errorf("probe: %s (exit %d)", k.Err, r.ExitCode)
	}
	return k, nil
}

// TestSnapshotKernelState measures which kernel secrets copies restored
// from one base still share after their load's reseed (ADR-030 (d), ledger
// H24, issue #157). Three copies are restored and claimed; a cold-booted
// pooled VM is the control. In each, the probe (kstateProbe) reads the
// TCP ISN key (net_secret), the TCP timestamp offset key (ts_secret), the
// first ephemeral port, the KASLR base and the TFO key.
//
// Asserted: the measurement discriminates. A rerun in the same guest
// matches itself, and the cold-booted control (its own boot, its own
// secrets) differs from the copies. Recorded, not asserted: whether the
// copies share each value. ADR-030 accepts that they do: guest TCP never
// leaves its own per-VM gateway stack (TestDgramNICOnlyGatewayServices),
// so no tenant sees or reaches another copy's sequence numbers or ports.
func TestSnapshotKernelState(t *testing.T) {
	b := snapBackend(t, -1)
	probe := buildKstateProbe(t)
	ctx := context.Background()
	id := b.baseID(ctx, true)
	claim := func(name string, m *Machine) (sandbox.VM, kstate) {
		app := t.TempDir()
		os.WriteFile(filepath.Join(app, "w.js"), []byte(identJS), 0o600)
		os.WriteFile(filepath.Join(app, "config.capnp"), []byte(markerCapnp), 0o600)
		pb, _ := os.ReadFile(probe)
		os.WriteFile(filepath.Join(app, "kstate"), pb, 0o755)
		spec := sandbox.Spec{ID: name, AppDir: app, WorkerdArgs: []string{"serve", "config.capnp", "--experimental"}, HealthCF: `{}`, Debug: true}
		vm, err := b.claim(ctx, m, spec)
		if err != nil {
			t.Fatalf("%s: claim: %v", name, err)
		}
		guestGet(t, vm, "/") // workerd is up and has served once, as in production
		k, err := runKstate(ctx, vm)
		if err != nil {
			vm.Stop()
			t.Fatalf("%s: %v", name, err)
		}
		return vm, k
	}

	const copies = 3
	var ks []kstate
	var first sandbox.VM
	for i := 0; i < copies; i++ {
		m, err := b.boot(ctx, bootOpts{name: fmt.Sprintf("ks%d", i), restore: id})
		if err != nil {
			t.Fatal(err)
		}
		vm, k := claim(fmt.Sprintf("kscopy%d", i), m)
		ks = append(ks, k)
		if i == 0 {
			first = vm
			defer vm.Stop()
		} else {
			vm.Stop()
		}
	}
	rerun, err := runKstate(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := b.boot(ctx, bootOpts{name: "kscold", pool: true})
	if err != nil {
		t.Fatal(err)
	}
	cvm, cold := claim("kscold", cm)
	defer cvm.Stop()

	for i, k := range ks {
		t.Logf("copy %d: isnKey %08x tsKey %08x eport %d _text %s tfo %s", i, k.isnKey(), k.tsKey(), k.EPort, k.Text, k.TFOKey)
	}
	t.Logf("copy 0 rerun: isnKey %08x tsKey %08x eport %d", rerun.isnKey(), rerun.tsKey(), rerun.EPort)
	t.Logf("cold control: isnKey %08x tsKey %08x eport %d _text %s tfo %s", cold.isnKey(), cold.tsKey(), cold.EPort, cold.Text, cold.TFOKey)

	// The method discriminates: same guest → same keys; another boot → not.
	if !sameNetSecret(ks[0], rerun) || !sameTSSecret(ks[0], rerun) {
		t.Fatalf("a rerun in the same guest gave other keys (isn %08x/%08x ts %08x/%08x): the probe doesn't measure the secret",
			ks[0].isnKey(), rerun.isnKey(), ks[0].tsKey(), rerun.tsKey())
	}
	if sameNetSecret(ks[0], cold) {
		t.Fatalf("the cold-booted control has the copies' ISN key (%08x/%08x): the probe can't tell secrets apart",
			ks[0].isnKey(), cold.isnKey())
	}

	// Recorded: what the copies share after the load (ADR-030 (d)).
	share := map[string]bool{"net_secret (ISN, ephemeral port offset)": true, "ts_secret (TCP timestamp offset)": true,
		"first ephemeral port": true, "KASLR _text": true, "TFO key": true}
	for i := 1; i < copies; i++ {
		share["net_secret (ISN, ephemeral port offset)"] = share["net_secret (ISN, ephemeral port offset)"] && sameNetSecret(ks[0], ks[i])
		share["ts_secret (TCP timestamp offset)"] = share["ts_secret (TCP timestamp offset)"] && sameTSSecret(ks[0], ks[i])
		share["first ephemeral port"] = share["first ephemeral port"] && ks[0].EPort == ks[i].EPort
		share["KASLR _text"] = share["KASLR _text"] && ks[0].Text == ks[i].Text
		share["TFO key"] = share["TFO key"] && ks[0].TFOKey == ks[i].TFOKey
	}
	for _, k := range []string{"net_secret (ISN, ephemeral port offset)", "ts_secret (TCP timestamp offset)", "first ephemeral port", "KASLR _text", "TFO key"} {
		t.Logf("shared by all %d copies after their load: %-40s %v", copies, k, share[k])
	}
	t.Logf("cold control vs copies: same KASLR _text %v (Firecracker boots vmlinux directly, so no KASLR slide unless the kernel randomizes itself); popcount(isnKey xor) %d",
		cold.Text == ks[0].Text, bits.OnesCount32(cold.isnKey()^ks[0].isnKey()))
}
