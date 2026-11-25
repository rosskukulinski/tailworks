package hostd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// A sandboxed Node app's listener is reachable only through tailworks'
// ingress (ledger H-lan-bind). Both sandboxes match TCP binds by port, not
// address, so an app that listens on 0.0.0.0:<PORT> (Node's listen(PORT)
// does when the app ignores HOST) would be reachable from the LAN. So the
// app may not create a listening TCP socket at all:
//
//   - Landlock (Linux): hostd creates the listener (127.0.0.1:<PORT>, or the
//     unix socket in listen "unix" mode) and passes it as fd 3; the profile
//     has no TCP bind port and seccomp turns every listen() into a no-op
//     (NoListen), so a socket the app binds itself, or an unbound one
//     (listen() picks *:<ephemeral>), never listens.
//   - Seatbelt (macOS): the profile allows no TCP bind or inbound at all
//     (Seatbelt can't keep the inherited 127.0.0.1 socket and refuse
//     0.0.0.0 on the same port), so the app listens on a unix socket in its
//     instance dir. hostd's ingress dials that socket, and hostd forwards
//     127.0.0.1:<PORT> to it for the app's self-calls.
//
// The preload (nodeListenPreloadJS) points the app's listen(PORT) or
// listen(TW_SOCKET) at fd 3 or the socket, so apps need no change. It is a
// convenience, not the boundary: hostile code that skips it gets a socket
// that doesn't listen (Linux) or EPERM (macOS).

const (
	nodeListenFD      = 3
	nodeListenPreload = "tw-listen.cjs"
	nodeListenSock    = "app-tcp.sock"
)

const (
	nodeListenNone = ""
	nodeListenFDs  = "fd"   // Landlock: inherited listener
	nodeListenUnix = "unix" // Seatbelt, tcp mode: own unix socket + forwarder
)

// nodeListenMode is how the app's listener is confined under sandbox.
func nodeListenMode(sandbox, listen string) string {
	switch {
	case sandbox == SandboxLandlock:
		return nodeListenFDs
	case sandbox == SandboxSeatbelt && listen != "unix":
		return nodeListenUnix
	}
	return nodeListenNone
}

// nodeListener is hostd's side of the app's listener.
type nodeListener struct {
	mode string
	ln   net.Listener // fd mode: passed to every process start
	sock string       // unix mode: where the app listens
	fwd  net.Listener // unix mode: 127.0.0.1:<PORT> → sock
	wg   sync.WaitGroup
}

// setupNodeListen prepares np's listener for the host's sandbox (before the
// first start). It may replace np.port.
func (inst *instance) setupNodeListen() error {
	np := inst.node
	nl := &nodeListener{mode: nodeListenMode(inst.h.sandbox, np.listen)}
	np.nl = nl
	switch nl.mode {
	case nodeListenFDs:
		var err error
		if np.listen == "unix" {
			os.Remove(np.addr)
			nl.ln, err = net.Listen("unix", np.addr)
		} else {
			nl.ln, err = net.Listen("tcp4", "127.0.0.1:0")
			if err == nil {
				np.port = nl.ln.Addr().(*net.TCPAddr).Port
				np.addr = "127.0.0.1:" + strconv.Itoa(np.port)
			}
		}
		return err
	case nodeListenUnix:
		nl.sock = filepath.Join(inst.dir, nodeListenSock)
		fwd, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		nl.fwd = fwd
		np.port = fwd.Addr().(*net.TCPAddr).Port
		np.addr = "127.0.0.1:" + strconv.Itoa(np.port)
		nl.wg.Add(1)
		go nl.forward()
	}
	return nil
}

// dial connects to the app (ingress, health checks).
func (np *nodeProc) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	switch {
	case np.nl != nil && np.nl.ln != nil:
		// hostd holds the listener, so a connect succeeds (and waits in the
		// backlog) even while the app is down: answer 503 instead.
		np.mu.Lock()
		up := np.pid != 0
		np.mu.Unlock()
		if !up {
			return nil, errors.New("node app is not running")
		}
	case np.nl != nil && np.nl.sock != "":
		return d.DialContext(ctx, "unix", np.nl.sock)
	case np.listen == "unix":
		return d.DialContext(ctx, "unix", np.addr)
	}
	return d.DialContext(ctx, "tcp", np.addr)
}

// confined reports whether the app gets no TCP bind port of its own.
func (np *nodeProc) confined() bool { return np.nl != nil && np.nl.mode != nodeListenNone }

// prepareSpawn writes the preload and adds its env, and in fd mode the
// listener, to a process start. It returns node's extra arguments.
func (np *nodeProc) prepareSpawn(dir string, env map[string]string) ([]string, error) {
	if !np.confined() {
		return nil, nil
	}
	pp := filepath.Join(dir, nodeListenPreload)
	if err := os.WriteFile(pp, []byte(nodeListenPreloadJS), 0o600); err != nil {
		return nil, err
	}
	if np.nl.mode == nodeListenFDs {
		env["TW_LISTEN_FD"] = strconv.Itoa(nodeListenFD)
	} else {
		os.Remove(np.nl.sock)
		env["TW_LISTEN_PATH"] = np.nl.sock
	}
	return []string{"--require", pp}, nil
}

// attach passes the fd-mode listener as fd 3.
func (np *nodeProc) attach(cmd *exec.Cmd) error {
	if np.nl == nil || np.nl.ln == nil {
		return nil
	}
	f, err := np.nl.ln.(interface{ File() (*os.File, error) }).File()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{f}
	return nil
}

// close stops the listener and the forwarder.
func (nl *nodeListener) close() {
	if nl == nil {
		return
	}
	if nl.ln != nil {
		nl.ln.Close()
	}
	if nl.fwd != nil {
		nl.fwd.Close()
		nl.wg.Wait()
	}
	if nl.sock != "" {
		os.Remove(nl.sock)
	}
}

// forward relays 127.0.0.1:<PORT> to the app's unix socket (self-calls).
func (nl *nodeListener) forward() {
	defer nl.wg.Done()
	for {
		c, err := nl.fwd.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.DialTimeout("unix", nl.sock, 10*time.Second)
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{})
			go func() {
				io.Copy(u, c)
				u.(*net.UnixConn).CloseWrite()
				close(done)
			}()
			io.Copy(c, u)
			c.(*net.TCPConn).CloseWrite()
			<-done
		}()
	}
}

// nodeListenPreloadJS points the app's own listen at hostd's listener:
// listen(PORT[, host]) and listen({port: PORT}) (tcp mode) or
// listen(TW_SOCKET) (unix mode) become listen({fd: 3}) or
// listen({path: TW_LISTEN_PATH}); any other listen is left alone (and the
// sandbox refuses it). server.address() keeps answering 127.0.0.1:<PORT>.
const nodeListenPreloadJS = `"use strict";
(() => {
  const net = require("net");
  const fd = process.env.TW_LISTEN_FD, path = process.env.TW_LISTEN_PATH;
  const port = Number(process.env.PORT), sock = process.env.TW_SOCKET;
  delete process.env.TW_LISTEN_FD;
  delete process.env.TW_LISTEN_PATH;
  if (!fd && !path) return;
  const own = (a) => {
    if (a !== null && typeof a === "object") {
      if (a.port !== undefined) return Number(a.port) === port;
      if (a.path !== undefined) return !!sock && a.path === sock;
      return false;
    }
    if (typeof a === "number" || (typeof a === "string" && /^[0-9]+$/.test(a))) return Number(a) === port;
    return typeof a === "string" && !!sock && a === sock;
  };
  const listen = net.Server.prototype.listen;
  net.Server.prototype.listen = function (...args) {
    if (!own(args[0])) return listen.apply(this, args);
    const cb = typeof args[args.length - 1] === "function" ? args[args.length - 1] : undefined;
    const opts = fd ? { fd: Number(fd) } : { path };
    if (path && port) {
      const addr = { address: "127.0.0.1", family: "IPv4", port };
      this.address = function () { return this.listening ? addr : null; };
    }
    return cb ? listen.call(this, opts, cb) : listen.call(this, opts);
  };
})();
`
