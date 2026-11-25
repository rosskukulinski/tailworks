//go:build e2edataplane

package dataplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/chaoskit"
	"github.com/rosskukulinski/tailworks/e2e/internal/mgmtpg"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// mgmtPG is the cluster's mgmt as n tw-mgmt replicas sharing one Postgres
// (TW_DP_MGMT=pg2|pg3), as in e2e/multimgmt, whose pieces it reuses
// (e2e/internal/mgmtpg, e2e/chaoskit): a throwaway Postgres container, one fault proxy
// per replica between it and Postgres, and a fake setec custody the
// replicas share. The cluster's mgmt socket (c.sock) becomes a front that
// spreads connections over the running replicas, so hosts (through their
// cuttable proxies) and the test's own calls fail over when one dies.
// Clients that must use one replica (the deploy loop's) call tryOn.
type mgmtPG struct {
	n       int
	pg      *mgmtpg.Postgres
	setec   *mgmtpg.FakeSetec
	proxies []*chaoskit.Proxy // replica i's path to Postgres
	socks   []string          // replica i's own socket
	rr      int
	mu      sync.Mutex
	clients map[int]*http.Client
}

// usePG reports whether mgmt runs as replicas on Postgres. On the first
// start it reads TW_DP_MGMT (sqlite, the default, or pg2/pg3), unless the
// test has already set c.pg.
func (c *cluster) usePG(first bool) bool {
	if first && c.pg == nil {
		switch m := envOr("TW_DP_MGMT", "sqlite"); m {
		case "sqlite":
		case "pg2", "pg3":
			n, _ := strconv.Atoi(m[2:])
			c.pg = &mgmtPG{n: n}
		default:
			c.t.Fatalf("TW_DP_MGMT %q: one of sqlite, pg2, pg3", m)
		}
	}
	return c.pg != nil
}

func replicaName(i int) string { return fmt.Sprintf("mgmt-r%d", i) }

// startMgmtPG starts every replica that is not running. The first time it
// also starts Postgres, the fake setec, the proxies and the front socket,
// and writes the host token every replica and host share.
func (c *cluster) startMgmtPG(first bool) {
	p := c.pg
	if first {
		p.pg = mgmtpg.Start(c.t, mgmtpg.Options{Name: fmt.Sprintf("tw-dp-pg-%d", os.Getpid()), Ctx: envOr("PG_CTX", "orbstack"),
			LogDir: c.dir, Record: func(kind, name string, pid int) { c.record(kind+":"+name, pid) }})
		p.setec = mgmtpg.NewFakeSetec(c.t)
		for i := 0; i < p.n; i++ {
			px, err := chaoskit.Listen(mgmtpg.Host(c.t, p.pg.DSN))
			if err != nil {
				c.t.Fatal(err)
			}
			c.t.Cleanup(px.Close)
			p.proxies = append(p.proxies, px)
			p.socks = append(p.socks, filepath.Join(c.work, fmt.Sprintf("r%d.sock", i)))
		}
		tok := strings.Repeat("ab", 32)
		if err := os.WriteFile(filepath.Join(c.work, "token"), []byte(tok+"\n"), 0o600); err != nil {
			c.t.Fatal(err)
		}
		c.mu.Lock()
		c.token = tok
		c.mu.Unlock()
		c.startFront()
	}
	var wg sync.WaitGroup
	for i := 0; i < p.n; i++ {
		if c.pid(replicaName(i)) != 0 {
			continue
		}
		c.startReplica(i)
		wg.Add(1)
		go func() { defer wg.Done(); c.waitReplica(i) }()
	}
	wg.Wait()
}

func (c *cluster) startReplica(i int) {
	p := c.pg
	os.Remove(p.socks[i])
	c.start(replicaName(i), "-local-dev", "-socket", p.socks[i], "-host-token-file", filepath.Join(c.work, "token"),
		"-state-dir", filepath.Join(c.work, replicaName(i)), "-db", mgmtpg.ViaProxy(c.t, p.pg.DSN, p.proxies[i].Addr()),
		"-custody", "setec", "-setec-url", p.setec.URL, "-tsapi-url", c.tsURL, "-reap-interval", "1h", "-no-loopback")
}

func (c *cluster) waitReplica(i int) {
	for j := 0; ; j++ {
		if st, _, err := c.tryOn(i, "GET", "/v1/hosts", nil, nil); err == nil && st == 200 {
			return
		}
		if j > 600 {
			c.dumpLogs()
			c.t.Errorf("replica %d did not start", i)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startFront serves c.sock: each connection goes to the next replica that
// accepts it.
func (c *cluster) startFront() {
	ln, err := net.Listen("unix", c.sock)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			cn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				up := c.pg.dialAny()
				if up == nil {
					cn.Close()
					return
				}
				go func() { io.Copy(up, cn); up.Close() }()
				io.Copy(cn, up)
				cn.Close()
			}()
		}
	}()
}

func (p *mgmtPG) dialAny() net.Conn {
	p.mu.Lock()
	start := p.rr
	p.rr++
	p.mu.Unlock()
	for k := 0; k < p.n; k++ {
		if u, err := net.DialTimeout("unix", p.socks[(start+k)%p.n], time.Second); err == nil {
			return u
		}
	}
	return nil
}

// replicas is how many mgmt processes there are (1 on SQLite).
func (c *cluster) replicas() int {
	if c.pg == nil {
		return 1
	}
	return c.pg.n
}

// client is replica i's HTTP client (on SQLite, mgmt's).
func (c *cluster) client(i int) *http.Client {
	if c.pg == nil {
		return c.hc
	}
	p := c.pg
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = map[int]*http.Client{}
	}
	if hc := p.clients[i]; hc != nil {
		return hc
	}
	sock := p.socks[i]
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	p.clients[i] = hc
	return hc
}

// tryOn is c.try through replica i.
func (c *cluster) tryOn(i int, method, path string, body, out any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, "http://mgmt"+path, rd)
	c.mu.Lock()
	rq.Header.Set(mgmt.HostTokenHeader, c.token)
	c.mu.Unlock()
	rq.Header.Set("content-type", "application/json")
	resp, err := c.client(i).Do(rq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		json.Unmarshal(b, out)
	}
	return resp.StatusCode, b, nil
}

// restartReplica is restart-mgmt with replicas: SIGKILL or SIGTERM one of
// them and start it again 0.5–5 s later. The others keep serving.
func (n *nemesis) restartReplica() string {
	i := n.rnd.Intn(n.c.pg.n)
	crash := n.rnd.Intn(2) == 0
	down := n.between(500*time.Millisecond, 5*time.Second)
	n.c.stop(replicaName(i), crash)
	time.Sleep(down)
	n.c.startMgmtPG(false)
	return fmt.Sprintf("r%d crash=%v down %v", i, crash, down)
}

// pgFault puts one replica's path to Postgres in a fault mode for a while:
// blackhole (2–10 s: every query hangs), delay (200 ms–1.5 s per read for
// 5–15 s) or reset (every connection dropped, new ones refused for 1–4 s).
func (n *nemesis) pgFault() string {
	if n.c.pg == nil {
		return "skipped: mgmt on SQLite"
	}
	i := n.rnd.Intn(n.c.pg.n)
	px := n.c.pg.proxies[i]
	defer px.Set("pass", 0)
	switch n.rnd.Intn(3) {
	case 0:
		d := n.between(2*time.Second, 10*time.Second)
		px.Set("blackhole", 0)
		time.Sleep(d)
		px.Set("pass", 0)
		return fmt.Sprintf("r%d blackhole %v", i, d)
	case 1:
		per, d := n.between(200*time.Millisecond, 1500*time.Millisecond), n.between(5*time.Second, 15*time.Second)
		px.Set("delay", per)
		time.Sleep(d)
		return fmt.Sprintf("r%d delay %v per read for %v", i, per, d)
	default:
		d := n.between(time.Second, 4*time.Second)
		px.Set("reset", 0)
		time.Sleep(d)
		return fmt.Sprintf("r%d reset for %v", i, d)
	}
}
