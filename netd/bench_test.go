package netd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// BenchmarkRegistryNodes measures what one object node costs a host (issue
// #114, ADR-006): the resident memory of a process holding N netd object
// nodes, idle and under request load, on hermetic testcontrol. Two peer
// visibilities: peers=scoped, where each object sees only its caller (the
// load generator; grants that keep objects from seeing one another, RFC
// §9), and peers=mesh, where every node sees every other (the worst case;
// netmap memory grows with N per node, so O(N²) in all).
//
// The registry runs in a child process (this test binary re-executed as
// TestBenchNodeChild), so its RSS holds only netd and tsnet: testcontrol,
// DERP, STUN and the load generator's own client node stay in the parent.
// Per-node figures are (RSS - RSS before the first join) / N.
//
// Each iteration is a whole scenario, so run it with -benchtime=1x
// (`make bench`). Knobs: TW_BENCH_SETTLE (idle wait after the last join,
// default 15s), TW_BENCH_LOAD (load duration, default 10s),
// TW_BENCH_CONC (concurrent requests, default 32).
func BenchmarkRegistryNodes(b *testing.B) {
	for _, mesh := range []bool{false, true} {
		vis := map[bool]string{false: "scoped", true: "mesh"}[mesh]
		for _, n := range []int{10, 100, 500} {
			b.Run(fmt.Sprintf("peers=%s/N=%d", vis, n), func(b *testing.B) { benchNodes(b, n, mesh, vis) })
		}
	}
}

func benchNodes(b *testing.B, n int, mesh bool, vis string) {
	var r benchResult
	for range b.N {
		r = runNodeBench(b, n, mesh)
	}
	b.ReportMetric(0, "ns/op") // a scenario, not an op
	b.ReportMetric(r.baseMB, "base-MB")
	b.ReportMetric(r.joinPeakMB, "join-peak-MB")
	b.ReportMetric(r.idlePerNode, "idle-MB/node")
	b.ReportMetric(r.loadPerNode, "load-MB/node")
	b.ReportMetric(r.heapPerNode, "heap-MB/node")
	b.ReportMetric(float64(r.peers), "peers/node")
	b.ReportMetric(r.joinSec, "join-s")
	b.ReportMetric(r.reqPerSec, "req/s")
	b.ReportMetric(r.p50ms, "p50-ms")
	b.ReportMetric(r.p99ms, "p99-ms")
	b.Logf("%s/%s peers=%s N=%d: base %.1f MB; idle %.1f MB (%.2f MB/node); load peak %.1f MB (%.2f MB/node); join peak %.1f MB; Go heap %.2f MB/node; goroutines %d (%.0f/node); join %.1f s (%.0f nodes/min); %.0f req/s at c=%d, p50 %.2f ms, p99 %.2f ms, %d errors",
		runtime.GOOS, runtime.GOARCH, vis, n, r.baseMB, r.idleMB, r.idlePerNode, r.loadMB, r.loadPerNode, r.joinPeakMB,
		r.heapPerNode, r.goroutines, float64(r.goroutines)/float64(n), r.joinSec, float64(n)/r.joinSec*60, r.reqPerSec, r.conc, r.p50ms, r.p99ms, r.errors)
}

type benchResult struct {
	baseMB, idleMB, loadMB, joinPeakMB    float64
	idlePerNode, loadPerNode, heapPerNode float64
	joinSec, reqPerSec, p50ms, p99ms      float64
	goroutines, conc, errors, peers       int
}

// benchMsg is one line from the child, prefixed with benchTag on stdout.
type benchMsg struct {
	Phase      string   `json:"phase"`
	IPs        []string `json:"ips,omitempty"`
	JoinSec    float64  `json:"joinSec,omitempty"`
	Failed     int      `json:"failed,omitempty"`
	HeapMB     float64  `json:"heapMB,omitempty"`
	Goroutines int      `json:"goroutines,omitempty"`
	Peers      int      `json:"peers,omitempty"` // in the first object's netmap
	Err        string   `json:"err,omitempty"`
}

const benchTag = "TWBENCH "

func envDur(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil {
		return d
	}
	return def
}

func runNodeBench(b *testing.B, n int, mesh bool) benchResult {
	b.Helper()
	settle, load := envDur("TW_BENCH_SETTLE", 15*time.Second), envDur("TW_BENCH_LOAD", 10*time.Second)
	conc, _ := strconv.Atoi(os.Getenv("TW_BENCH_CONC"))
	if conc <= 0 {
		conc = 32
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		b.Fatal(err)
	}
	defer ctl.Close()
	if !mesh {
		scopePeers(ctl.Server, "bench-client")
	}
	dir, err := os.MkdirTemp("/tmp", "tw-bench-")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	client, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "bench-client", filepath.Join(dir, "client"))
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestBenchNodeChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "TW_BENCH_CHILD=1", "TW_BENCH_CONTROL="+ctl.URL,
		"TW_BENCH_N="+strconv.Itoa(n), "TW_BENCH_DIR="+filepath.Join(dir, "child"))
	cmd.Stderr = os.Stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	msgs := make(chan benchMsg, 4)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), benchTag)
			if !ok {
				continue
			}
			var m benchMsg
			if json.Unmarshal([]byte(line), &m) == nil {
				msgs <- m
			}
		}
		close(msgs)
	}()
	recv := func(want string) benchMsg {
		select {
		case m, ok := <-msgs:
			if !ok {
				b.Fatalf("child exited before %q", want)
			}
			if m.Err != "" || m.Phase != want {
				b.Fatalf("child: want %q, got %+v", want, m)
			}
			return m
		case <-ctx.Done():
			b.Fatalf("timed out waiting for child %q", want)
		}
		return benchMsg{}
	}
	pid := cmd.Process.Pid
	rss := func() float64 {
		mb, err := processRSSMB(pid)
		if err != nil {
			b.Fatal(err)
		}
		return mb
	}

	base := recv("base")
	time.Sleep(2 * time.Second)
	var r benchResult
	r.conc = conc
	r.baseMB = medianRSS(rss, 5, 200*time.Millisecond)

	// Join all N, sampling the peak.
	stopPeak := make(chan struct{})
	peak := sampler(rss, stopPeak)
	io.WriteString(stdin, "join\n")
	ready := recv("ready")
	r.joinSec = ready.JoinSec
	if ready.Failed > 0 {
		b.Fatalf("%d/%d nodes failed to join", ready.Failed, n)
	}
	if err := waitAllPeers(ctx, client, ready.IPs); err != nil {
		b.Fatal(err)
	}
	close(stopPeak)
	r.joinPeakMB = <-peak

	// Idle.
	time.Sleep(settle)
	r.idleMB = medianRSS(rss, 10, 500*time.Millisecond)
	io.WriteString(stdin, "stats\n")
	st := recv("stats")
	r.goroutines = st.Goroutines
	r.peers = st.Peers
	if want := map[bool]int{false: 1, true: n}[mesh]; st.Peers != want {
		b.Fatalf("an object sees %d peers, want %d", st.Peers, want)
	}
	r.heapPerNode = (st.HeapMB - base.HeapMB) / float64(n)

	// Load: conc workers round-robin over every node, keep-alive conns.
	hc := client.HTTPClient(10 * time.Second)
	hc.Transport.(*http.Transport).MaxIdleConnsPerHost = 2
	var next, errs atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration
	stopLoad := make(chan struct{})
	peak = sampler(rss, stopLoad)
	lctx, lcancel := context.WithTimeout(ctx, load)
	var wg sync.WaitGroup
	start := time.Now()
	for range conc {
		wg.Go(func() {
			var mine []time.Duration
			for lctx.Err() == nil {
				ip := ready.IPs[int(next.Add(1))%len(ready.IPs)]
				t0 := time.Now()
				req, _ := http.NewRequestWithContext(lctx, http.MethodGet, "http://"+ip+"/", nil)
				resp, err := hc.Do(req)
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						err = fmt.Errorf("status %d", resp.StatusCode)
					}
				}
				if lctx.Err() != nil {
					break
				}
				if err != nil {
					errs.Add(1)
					continue
				}
				mine = append(mine, time.Since(t0))
			}
			mu.Lock()
			lat = append(lat, mine...)
			mu.Unlock()
		})
	}
	wg.Wait()
	lcancel()
	elapsed := time.Since(start)
	close(stopLoad)
	r.loadMB = <-peak
	r.errors = int(errs.Load())
	slices.Sort(lat)
	if len(lat) > 0 {
		r.reqPerSec = float64(len(lat)) / elapsed.Seconds()
		r.p50ms = float64(lat[len(lat)/2].Microseconds()) / 1000
		r.p99ms = float64(lat[len(lat)*99/100].Microseconds()) / 1000
	}
	r.idlePerNode = (r.idleMB - r.baseMB) / float64(n)
	r.loadPerNode = (r.loadMB - r.baseMB) / float64(n)
	return r
}

// processRSSMB is pid's resident set size in MB (2^20 bytes), via ps(1) so
// that it works the same on Linux and macOS.
func processRSSMB(pid int) (float64, error) {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps %d: %w", pid, err)
	}
	kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ps %d: %q", pid, out)
	}
	return kb / 1024, nil
}

func medianRSS(rss func() float64, k int, every time.Duration) float64 {
	var v []float64
	for i := range k {
		if i > 0 {
			time.Sleep(every)
		}
		v = append(v, rss())
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// sampler records the peak of rss every 250 ms until stop is closed.
func sampler(rss func() float64, stop chan struct{}) <-chan float64 {
	out := make(chan float64, 1)
	go func() {
		peak := rss()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				out <- max(peak, rss())
				return
			case <-t.C:
				peak = max(peak, rss())
			}
		}
	}()
	return out
}

// waitAllPeers waits until every ip is in c's netmap (one Status per poll).
func waitAllPeers(ctx context.Context, c *nettest.Node, ips []string) error {
	for {
		st, err := c.LC.Status(ctx)
		if err == nil {
			seen := map[string]bool{}
			for _, p := range st.Peer {
				for _, a := range p.TailscaleIPs {
					seen[a.String()] = true
				}
			}
			missing := 0
			for _, ip := range ips {
				if !seen[ip] {
					missing++
				}
			}
			if missing == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("client never saw all %d nodes: %w", len(ips), ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// TestBenchNodeChild is BenchmarkRegistryNodes's registry process; it skips
// unless the benchmark started it. Protocol: it prints "base", then on
// "join" creates TW_BENCH_N objects and prints "ready" (their IPs) once all
// are ready; "stats" prints its Go heap and goroutines; EOF closes it.
func TestBenchNodeChild(t *testing.T) {
	if os.Getenv("TW_BENCH_CHILD") != "1" {
		t.Skip("run by BenchmarkRegistryNodes")
	}
	say := func(m benchMsg) {
		bs, _ := json.Marshal(m)
		fmt.Fprintf(os.Stdout, "%s%s\n", benchTag, bs)
	}
	n, _ := strconv.Atoi(os.Getenv("TW_BENCH_N"))
	reg, err := netd.New(netd.Config{ControlURL: os.Getenv("TW_BENCH_CONTROL"), StateRoot: os.Getenv("TW_BENCH_DIR"),
		Keys: netd.StaticKey(nettest.DummyKey), Ephemeral: true,
		Upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })})
	if err != nil {
		say(benchMsg{Err: err.Error()})
		t.Fatal(err)
	}
	defer reg.Close()
	say(benchMsg{Phase: "base", HeapMB: heapMB()})
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch sc.Text() {
		case "join":
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			st := time.Now()
			objs := make([]*netd.Object, n)
			for i := range n {
				if objs[i], err = reg.Create(fmt.Sprintf("b%04d", i), netd.ObjectOptions{}); err != nil {
					say(benchMsg{Err: err.Error()})
					t.Fatal(err)
				}
			}
			m := benchMsg{Phase: "ready"}
			for _, o := range objs {
				if o.Ready(ctx) != nil {
					m.Failed++
					continue
				}
				m.IPs = append(m.IPs, o.IPv4().String())
			}
			m.JoinSec = time.Since(st).Seconds()
			cancel()
			say(m)
		case "stats":
			m := benchMsg{Phase: "stats", HeapMB: heapMB(), Goroutines: runtime.NumGoroutine()}
			if o := reg.Get("b0000"); o != nil {
				if st, err := o.LocalClient().Status(context.Background()); err == nil {
					m.Peers = len(st.Peer)
				}
			}
			say(m)
		}
	}
}

func heapMB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapInuse) / (1 << 20)
}
