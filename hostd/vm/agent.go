package vm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
	"github.com/rosskukulinski/tailworks/hostd/sandbox"
	"github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/netd"
)

// agent is the host end of one guest: the tw-guestd control channel
// (guest/proto NDJSON over vsock), the host-side network stack and the
// ingress proxy through it. Both backends (Firecracker, vz) embed it; they
// differ only in how the VM, its vsock and its NIC come to exist.
type agent struct {
	log     *slog.Logger
	vn      *vnet
	ingress http.Handler

	info    sandbox.Info
	start   time.Time // the VM's start: Boot and Ready are measured from it
	done    chan struct{}
	ready   chan struct{}
	readyOK atomic.Bool
	// pool VMs (Firecracker warm pool / snapshot restores): closed when
	// the guest first says "pooled" (warm, waiting for its load)
	pooled   chan struct{}
	pooledOK atomic.Bool
	pooledAt time.Time
	// onBoot, if set, is called with the count of "boot" reports (a guest
	// that boots again was restarted by its hypervisor).
	onBoot func(n int)
	boots  int
	// Container guests: onEvent sees every guest event first (true =
	// consumed), and onStream gets each exec stdio connection (the guest
	// opens it with an ExecStreamHello line naming the exec and carrying
	// its stream token; onStream checks the token).
	onEvent  func(proto.Msg) bool
	onStream func(exec, token string, c net.Conn)

	// ctlToken is this boot's control-channel token (GuestConfig.CtlToken,
	// on the config drive): a guest connection is the control channel
	// only if its "hello" carries it. Anything else in the guest that can
	// open a vsock connection to the host (a container process that got
	// past its seccomp filter) can't take the channel over, forge guest
	// events or receive the host's ops. A pooled VM's (or a copy restored
	// from a base snapshot, which shares the base's) is replaced at claim
	// by a fresh one sent in the load (rekey); from then on only that one
	// is accepted. Guarded by mu.
	ctlToken string
	// ctlConns counts the control connections accepted this boot (> 1:
	// guestd reconnected after its connection died).
	ctlConns int

	mu      sync.Mutex
	ctl     net.Conn
	enc     *json.Encoder
	pending map[int64]chan proto.Msg
	nextID  int64
	events  []proto.Msg
	lastErr string
}

func newAgent() agent {
	return agent{done: make(chan struct{}), ready: make(chan struct{}), pooled: make(chan struct{}), pending: map[int64]chan proto.Msg{}, ctlToken: newToken()}
}

// rekey replaces the control-channel token with a fresh one and returns it
// (a pooled VM's claim: the load carries it to guestd). Connections that
// present the old token are refused from now on; the current channel,
// already authenticated, stays.
func (m *agent) rekey() string {
	t := newToken()
	m.mu.Lock()
	m.ctlToken = t
	m.mu.Unlock()
	return t
}

// token is the control-channel token currently accepted.
func (m *agent) token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctlToken
}

// newToken is a random 256-bit token (hex).
func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// tokenEqual compares a token the guest sent with the expected one in
// constant time ("" never matches).
func tokenEqual(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ---- control channel ----

func (m *agent) acceptCtl(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go m.route(c)
	}
}

// route reads a guest connection's first line: an exec stdio stream
// (container guests) says so and carries its stream token (onStream checks
// it); a control connection is guestd's "hello" with this boot's ctlToken.
// Anything else is closed. guestd reconnects whenever its connection dies
// (a vsock transport reset, e.g. around a VMM snapshot such as the
// keyscan's memory dump, would do that), so an authenticated hello
// replaces the current control channel; an unauthenticated one never
// touches it.
func (m *agent) route(c net.Conn) {
	br := bufio.NewReaderSize(c, 64<<10)
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := br.ReadSlice('\n')
	c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var first proto.Msg
	if json.Unmarshal(line, &first) != nil {
		c.Close()
		return
	}
	switch first.Type {
	case proto.ExecStreamHello:
		if m.onStream == nil {
			c.Close()
			return
		}
		m.onStream(first.Exec, first.Token, &bufConn{Conn: c, r: br})
		return
	case "hello":
		if tokenEqual(first.Token, m.token()) {
			if p := first.HelloProto(); p != version.GuestProtocol && m.log != nil {
				// The guest image and hostd ship in one release; another
				// protocol means a mixed install (an upgrade in progress,
				// or a hand-built guest). Ops newer than the guest's
				// protocol get "unknown op" (proto.UnknownOp): feature absent.
				m.log.Warn("guest speaks a different protocol", "guestProto", p, "hostProto", version.GuestProtocol,
					"guestVersion", first.GuestVersion, "hostVersion", version.Version)
			}
			break
		}
		fallthrough
	default:
		if m.log != nil {
			m.log.Warn("refused an unauthenticated guest vsock connection", "type", first.Type)
		}
		c.Close()
		return
	}
	m.mu.Lock()
	old := m.ctl
	m.ctl, m.enc = c, json.NewEncoder(c)
	m.ctlConns++
	n := m.ctlConns
	m.mu.Unlock()
	if old != nil {
		old.Close()
		if m.log != nil {
			m.log.Info("guest control channel reconnected", "connections", n)
		}
	}
	m.readCtl(io.MultiReader(bytes.NewReader(append([]byte(nil), line...)), br))
}

// bufConn is a conn whose first bytes were read into r.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (m *agent) readCtl(c io.Reader) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var msg proto.Msg
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		m.mu.Lock()
		if msg.Type == "resp" {
			if ch := m.pending[msg.ID]; ch != nil {
				delete(m.pending, msg.ID)
				ch <- msg
			}
			m.mu.Unlock()
			continue
		}
		if len(m.events) < 256 && msg.Type != "log" {
			m.events = append(m.events, msg)
		}
		m.mu.Unlock()
		if m.onEvent != nil && m.onEvent(msg) {
			continue
		}
		switch msg.Type {
		case "boot":
			m.boots++
			if m.boots == 1 {
				m.mu.Lock()
				m.info.Boot = time.Since(m.start)
				m.mu.Unlock()
			}
			m.setGuestStages(msg.Stages)
			if m.onBoot != nil {
				m.onBoot(m.boots)
			}
		case "pooled":
			m.setGuestStages(msg.Stages)
			if m.pooledOK.CompareAndSwap(false, true) {
				m.mu.Lock()
				m.pooledAt = time.Now()
				m.mu.Unlock()
				close(m.pooled)
			}
		case "ready":
			m.mu.Lock()
			if src := m.info.Source; src != "pool" && src != "restore" {
				m.info.Ready = time.Since(m.start) // a claim sets its own
			}
			m.info.GuestUptimeAtReady = time.Duration(msg.UptimeMS) * time.Millisecond
			m.mu.Unlock()
			m.setGuestStages(msg.Stages)
			if m.readyOK.CompareAndSwap(false, true) {
				close(m.ready)
			}
		case "workerd-exit":
			m.log.Warn("guest workerd exited", "code", msg.ExitCode, "restarts", msg.Restarts, "err", msg.Error)
			m.mu.Lock()
			m.lastErr = fmt.Sprintf("workerd exit %d %s", msg.ExitCode, msg.Error)
			m.mu.Unlock()
		}
	}
}

func (m *agent) setGuestStages(st map[string]float64) {
	if len(st) == 0 {
		return
	}
	m.mu.Lock()
	if m.info.GuestStages == nil {
		m.info.GuestStages = map[string]float64{}
	}
	for k, v := range st {
		m.info.GuestStages[k] = v
	}
	m.mu.Unlock()
}

func (m *agent) eventTypes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s []string
	for _, e := range m.events {
		s = append(s, e.Type)
	}
	return s
}

// Op implements sandbox.VM.
func (m *agent) Op(ctx context.Context, msg proto.Msg) (proto.Msg, error) {
	ch := make(chan proto.Msg, 1)
	m.mu.Lock()
	if m.enc == nil {
		m.mu.Unlock()
		return proto.Msg{}, errors.New("guest agent not connected")
	}
	m.nextID++
	msg.ID = m.nextID
	m.pending[msg.ID] = ch
	err := m.enc.Encode(msg)
	m.mu.Unlock()
	if err != nil {
		return proto.Msg{}, err
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-ctx.Done():
		return proto.Msg{}, ctx.Err()
	case <-m.done:
		return proto.Msg{}, errors.New("microVM exited")
	}
}

// ---- ingress ----

func (m *agent) ingressProxy() http.Handler {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return m.vn.dialGuest(ctx, proto.IngressPort)
		},
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     netd.UpstreamIdleTimeout, // below workerd's idle close (#309)
	}
	target := &url.URL{Scheme: "http", Host: "guest"}
	return &httputil.ReverseProxy{
		Transport:     tr,
		FlushInterval: -1,
		ErrorLog:      nil,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			netd.KeepBodyEOF(pr) // #138
			pr.Out.Host = pr.In.Host
			netd.KeepForwarded(pr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			netd.WriteError(w, &netd.Error{Code: netd.CodeUpstreamUnavailable, Detail: "microVM: " + err.Error()})
		},
	}
}

// Ingress implements sandbox.VM.
func (m *agent) Ingress() http.Handler { return m.ingress }

// DialGuest implements sandbox.VM.
func (m *agent) DialGuest(ctx context.Context) (net.Conn, error) {
	return m.vn.dialGuest(ctx, proto.IngressPort)
}

// Done implements sandbox.VM.
func (m *agent) Done() <-chan struct{} { return m.done }

// Info implements sandbox.VM.
func (m *agent) Info() sandbox.Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.info
	i.Stages, i.GuestStages = maps.Clone(i.Stages), maps.Clone(i.GuestStages)
	return i
}

// Refused implements sandbox.VM.
func (m *agent) Refused() []string { return m.vn.refusedFlows() }

// lastError is the guest's last reported workerd failure.
func (m *agent) lastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}
