package hostd

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/obs"
	"github.com/rosskukulinski/tailworks/internal/wallclock"
	"github.com/rosskukulinski/tailworks/netd"
)

// Raw TCP egress from workerd (ADR-052): stock connect() from a
// Worker, a facet-mode or plain-DO object, and ctx.tailnet.connect(). The
// platform's connect() handlers open a CONNECT tunnel to the same egress
// endpoints fetch() uses (egress-obj for facets, egress-wl for the tenant
// service) and authenticate it in band (gen.ConnectPreamble). hostd dials
// with netd's DialEgress, as the Node tier's CONNECT proxy does
// (nodeConnect), so a socket gets fetch()'s policy, refusals and kill.

// maxSocketsPerObject caps one egress identity's open sockets (an object,
// or the workload node for the Worker and plain-DO objects) per instance.
const maxSocketsPerObject = 256

// connectPreambleTimeout bounds how long the platform has to send its
// preamble once hostd has accepted the CONNECT.
const connectPreambleTimeout = 10 * time.Second

// CodeSocketLimit refuses a socket over maxSocketsPerObject.
const CodeSocketLimit = "socket_limit"

type localTokenKey struct{}

// connectAuth checks a tunnel's preamble for dst and returns the object
// it egresses as.
type connectAuth func(ctx context.Context, p *gen.ConnectPreamble, dst string) (*object, *apiError)

// tcpFlows are an instance's open sockets, closed when it stops.
type tcpFlows struct {
	mu    sync.Mutex
	open  map[*tcpFlow]struct{}
	byObj map[string]int
	done  bool
}

type tcpFlow struct {
	c, up net.Conn
	obj   string
}

// add registers f, refusing it over the per-object cap or once the
// instance is stopping.
func (t *tcpFlows) add(f *tcpFlow) *apiError {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return &apiError{Code: "killed", Detail: "the instance is stopping"}
	}
	if t.byObj[f.obj] >= maxSocketsPerObject {
		return &apiError{Code: CodeSocketLimit, Detail: fmt.Sprintf("object %s already has %d open sockets", f.obj, maxSocketsPerObject)}
	}
	if t.open == nil {
		t.open, t.byObj = map[*tcpFlow]struct{}{}, map[string]int{}
	}
	t.open[f] = struct{}{}
	t.byObj[f.obj]++
	return nil
}

func (t *tcpFlows) remove(f *tcpFlow) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.open[f]; ok {
		delete(t.open, f)
		if t.byObj[f.obj]--; t.byObj[f.obj] <= 0 {
			delete(t.byObj, f.obj)
		}
	}
}

// count is obj's open sockets.
func (t *tcpFlows) count(obj string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byObj[obj]
}

// closeAll closes every open socket and refuses new ones (instance stop:
// redeploy, teardown).
func (t *tcpFlows) closeAll() {
	t.mu.Lock()
	t.done = true
	fs := make([]*tcpFlow, 0, len(t.open))
	for f := range t.open {
		fs = append(fs, &tcpFlow{c: f.c, up: f.up})
	}
	t.mu.Unlock()
	for _, f := range fs {
		f.close()
	}
}

// setUp records f's dialled upstream (nil-safe close before then).
func (t *tcpFlows) setUp(f *tcpFlow, up net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false
	}
	f.up = up
	return true
}

func (f *tcpFlow) close() {
	f.c.Close()
	if f.up != nil {
		f.up.Close()
	}
}

// serveConnect serves a CONNECT tunnel from the platform.
func (h *Host) serveConnect(w http.ResponseWriter, r *http.Request, inst *instance, auth connectAuth) {
	dst := r.Host
	hj, ok := w.(http.Hijacker)
	if !ok {
		netd.WriteError(w, &netd.Error{Code: netd.CodeBadRequest, Detail: "CONNECT needs HTTP/1.1"})
		return
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	// workerd sends nothing until it has its 200; the preamble follows.
	if _, err := brw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || brw.Flush() != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Now().Add(connectPreambleTimeout))
	var p gen.ConnectPreamble
	if err := gen.ReadConnectLine(brw.Reader, &p); err != nil || p.V != 1 {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	refuse := func(e *apiError) {
		gen.WriteConnectLine(c, gen.ConnectStatus{Code: e.Code, Message: e.Detail})
		c.Close()
	}
	if want, _ := r.Context().Value(localTokenKey{}).(string); !gen.ConnectTokenOK(&p, want) {
		refuse(&apiError{Code: "forbidden", Detail: "not this instance's workerd"})
		return
	}
	o, ae := auth(r.Context(), &p, dst)
	if ae != nil {
		refuse(ae)
		return
	}
	sp := h.tel.startConnect(inst, o, dst)
	f := &tcpFlow{c: c, obj: o.ID}
	if ae := inst.flows.add(f); ae != nil {
		sp.end(0, 0, ae.Code)
		h.audit.add(mgmtapi.AuditRecord{ObjectID: o.ID, Kind: "deny", Dst: dst, Code: ae.Code, TraceID: sp.traceID()})
		refuse(ae)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.ReadyTimeout)
	up, err := inst.w.tenant.reg.DialEgress(ctx, o.NetdID, dst)
	cancel()
	if err == nil && p.TLS {
		up, err = clientTLS(up, dst, h.cfg.ReadyTimeout)
	}
	if err != nil {
		inst.flows.remove(f)
		e, ok := netd.AsError(err)
		if !ok {
			e = &netd.Error{Code: netd.CodeDialFailed, Detail: err.Error(), Destination: dst}
		}
		sp.end(0, 0, e.Code)
		h.audit.add(mgmtapi.AuditRecord{ObjectID: o.ID, Kind: "deny", Dst: dst, Code: e.Code, TraceID: sp.traceID()})
		refuse(&apiError{Code: e.Code, Detail: e.Detail})
		return
	}
	if !inst.flows.setUp(f, up) {
		inst.flows.remove(f)
		up.Close()
		c.Close()
		return
	}
	if err := gen.WriteConnectLine(c, gen.ConnectStatus{OK: true}); err != nil {
		inst.flows.remove(f)
		up.Close()
		c.Close()
		return
	}
	go func() {
		out, in := spliceCount(c, brw.Reader, up)
		inst.flows.remove(f)
		sp.end(out, in, "")
		h.audit.add(mgmtapi.AuditRecord{ObjectID: o.ID, Kind: "egress", Dst: dst, TraceID: sp.traceID(),
			Detail: mgmtapi.Note(fmt.Sprintf("tcp: %d bytes sent, %d received, %s", out, in, sp.dur().Round(time.Millisecond)))})
	}()
}

// clientTLS originates TLS to dst over up (ctx.tailnet.connect({ tls: "netd" })).
func clientTLS(up net.Conn, dst string, timeout time.Duration) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(dst)
	tc := tls.Client(up, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		up.Close()
		return nil, &netd.Error{Code: netd.CodeDialFailed, Detail: "tls: " + err.Error(), Destination: dst}
	}
	return tc, nil
}

// spliceCount is splice (node.go), returning the bytes sent to up and
// received from it.
func spliceCount(c net.Conn, br *bufio.Reader, up net.Conn) (out, in int64) {
	var o, i atomic.Int64
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(up, br)
		o.Store(n)
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			up.Close()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(c, up)
		i.Store(n)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			c.Close()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	c.Close()
	up.Close()
	return o.Load(), i.Load()
}

// connectAsFacet authenticates a facet object's tunnel (its MAC), as
// egressObjHandler does a fetch().
func (h *Host) connectAsFacet(inst *instance) connectAuth {
	return func(ctx context.Context, p *gen.ConnectPreamble, dst string) (*object, *apiError) {
		cls, name, ok := gen.VerifyConnectOnce(inst.res.MACKey, p, dst, wallclock.Now(), &inst.nonces)
		if !ok {
			return nil, &apiError{Code: "unknown_object", Detail: "egress socket failed platform authentication"}
		}
		if c := inst.app.Classes[cls]; c == nil || !c.Facet() {
			return nil, &apiError{Code: "unknown_object", Detail: "no facet class " + cls}
		}
		o, err := h.facetEgressObject(ctx, inst, cls, name) // nodeless.go
		if err != nil {
			return nil, asAPIError(err)
		}
		h.mu.Lock()
		killed := o.Status == statusKilled
		h.mu.Unlock()
		if killed {
			return nil, &apiError{Code: "killed", Detail: "object " + o.ID + " was killed"}
		}
		return o, nil
	}
}

// connectAsWorkload: the tenant service's sockets leave as the workload node.
func (h *Host) connectAsWorkload(inst *instance) connectAuth {
	return func(_ context.Context, p *gen.ConnectPreamble, _ string) (*object, *apiError) {
		if p.Class != "" || p.MAC != "" {
			return nil, &apiError{Code: "bad_request", Detail: "the workload egress carries no object identity"}
		}
		o := inst.w.currentWL()
		if o == nil {
			return nil, &apiError{Code: "identity_pending", Detail: "workload node not created yet"}
		}
		return o, nil
	}
}

// connectSpan is a socket's CLIENT span, ended when the flow closes.
type connectSpan struct {
	t      *telemetry
	inst   *instance
	o      *object
	dst    string
	start  time.Time
	tc     obs.TraceContext
	closed time.Time
}

func (t *telemetry) startConnect(inst *instance, o *object, dst string) *connectSpan {
	return &connectSpan{t: t, inst: inst, o: o, dst: dst, start: time.Now(),
		tc: obs.TraceContext{TraceID: obs.NewTraceID(), SpanID: obs.NewSpanID(), Flags: "01"}}
}

func (s *connectSpan) traceID() string { return s.tc.TraceID }

func (s *connectSpan) dur() time.Duration { return s.closed.Sub(s.start) }

func (s *connectSpan) end(out, in int64, errCode string) {
	s.closed = time.Now()
	w, o := s.inst.w, s.o
	host, port, _ := net.SplitHostPort(s.dst)
	attrs := []obs.Attr{obs.A("network.transport", "tcp"), obs.A("server.address", host), obs.A("error.type", errCode),
		obs.A("tw.socket.bytes_sent", out), obs.A("tw.socket.bytes_received", in),
		obs.A(attrWorkload, w.name), obs.A(attrObject, o.ID), obs.A(attrClass, o.Class), obs.A("tw.object.name", o.Name)}
	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, obs.A("server.port", int64(p)))
	}
	st := obs.StatusUnset
	if errCode != "" {
		st = obs.StatusError
	}
	s.t.span(w, obs.Span{TraceID: s.tc.TraceID, SpanID: s.tc.SpanID, Name: "connect " + s.dst, Kind: obs.SpanKindClient,
		Start: s.start, End: s.closed, Attrs: attrs, Status: st, StatusMessage: errCode})
}

// OpenSockets is objectID's open connect() sockets in the workload's
// current instance.
func (h *Host) OpenSockets(tenantID, workload, objectID string) int {
	h.mu.Lock()
	w := h.workloads[tenantID+"/"+workload]
	h.mu.Unlock()
	if w == nil {
		return 0
	}
	inst := w.current()
	if inst == nil {
		return 0
	}
	return inst.flows.count(objectID)
}
