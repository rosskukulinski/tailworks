//go:build linux

package helper

import (
	"net"
	"runtime/debug"
)

// recoverConn turns a panic while serving one client connection into a
// logged error: the deferred cleanups in serveConn have already stopped
// that connection's VM and closed it, and the helper keeps serving every
// other connection (security/LOOP.md I8). It must be deferred directly.
func (s *Server) recoverConn(c *net.UnixConn) {
	if r := recover(); r != nil {
		s.log.Error("helper connection panicked; dropping it", "panic", r, "stack", string(debug.Stack()))
		c.Close()
	}
}
