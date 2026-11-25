package hostd

import (
	"net"
	"strconv"
)

// workerd's V8 inspector for tw dev --inspector-port (issue #42). Each
// workerd gets its own free 127.0.0.1 port: a reload starts the new
// instance before the old one drains (blue/green), so they can't share
// one. tw dev serves the fixed --inspector-port and forwards to the
// active instance's (InspectorAddr).

// inspectorArgs adds --inspector-addr to a workerd command line when
// Config.WorkerdInspector is on, and returns the TCP port the sandbox must
// let it bind (Landlock; nil when off).
func (inst *instance) inspectorArgs(args []string) ([]string, []uint16) {
	if !inst.h.cfg.WorkerdInspector || !inst.h.cfg.ForceProcessTier {
		return args, nil
	}
	inst.mu.Lock()
	addr := inst.inspector
	inst.mu.Unlock()
	if addr == "" { // a restart keeps its port
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			inst.h.log.Warn("inspector: no free port", "err", err)
			return args, nil
		}
		addr = ln.Addr().String()
		ln.Close()
		inst.mu.Lock()
		inst.inspector = addr
		inst.mu.Unlock()
	}
	_, ps, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(ps)
	return append(args, "--inspector-addr="+addr), []uint16{uint16(p)}
}

// InspectorAddr is the 127.0.0.1:port of the V8 inspector of the
// workload's active workerd ("" when Config.WorkerdInspector is off or no
// instance runs yet).
func (h *Host) InspectorAddr(tenantID, workload string) string {
	h.mu.Lock()
	w := h.workloads[tenantID+"/"+workload]
	h.mu.Unlock()
	if w == nil {
		return ""
	}
	inst := w.current()
	if inst == nil {
		return ""
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.inspector
}
