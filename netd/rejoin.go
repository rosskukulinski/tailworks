package netd

import (
	"strings"

	"tailscale.com/health"
	"tailscale.com/ipn"
)

// loginRejectedNotify reports whether an IPN bus message says control
// refused this node's login (watchNeedsLogin starts its grace timer on it).
//
// Two forms count. One is an ErrMessage, which the backend sends only for
// errors control itself returned. The other is the "login-state" health
// warning carrying a NodeKeyExpired login error. A saved node whose key
// control has expired loops in NoState on exactly that error and sends no
// ErrMessage and no NeedsLogin (issue #182). Network errors reaching control
// also set login-state. They don't count, so a host that restarts while
// control is unreachable keeps its nodes' identities.
func loginRejectedNotify(n *ipn.Notify) bool {
	if n.ErrMessage != nil && *n.ErrMessage != "" {
		return true
	}
	if n.Health == nil {
		return false
	}
	w, ok := n.Health.Warnings[health.LoginStateWarnable.Code]
	return ok && strings.Contains(w.Args[health.ArgError], "NodeKeyExpired")
}
