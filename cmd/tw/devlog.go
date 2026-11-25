package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"syscall"
)

// quietStdLog drops the standard logger's chatter that means nothing to a
// `tw dev` user: tailscale's stunserver logs "STUN server listening on …"
// through the global log when the embedded control plane starts. Every
// other line still reaches stderr.
func quietStdLog() {
	log.SetOutput(dropLines{w: os.Stderr, drop: [][]byte{[]byte("STUN server listening on ")}})
}

type dropLines struct {
	w    io.Writer
	drop [][]byte
}

// Write gets one whole line per call (log.Logger writes each entry once).
func (d dropLines) Write(p []byte) (int, error) {
	for _, s := range d.drop {
		if bytes.Contains(p, s) {
			return len(p), nil
		}
	}
	return d.w.Write(p)
}

// checkDevPort fails fast (before the bundle and the control plane start)
// when 127.0.0.1:port is taken, and warns when [::1]:port is: then
// `localhost:<port>` may reach that other server instead of tw dev.
func checkDevPort(port int, warn io.Writer) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fail("bad_request", fmt.Sprintf("port %d is in use on 127.0.0.1 (another `tw dev`, or `wrangler dev`, which uses 8787 too?)", port), err.Error()).
			withFix(fmt.Sprintf("tw dev --port %d", port+1), "or tw dev --port 0 (any free port)")
	}
	ln.Close()
	if ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port))); err == nil {
		ln6.Close()
	} else if errors.Is(err, syscall.EADDRINUSE) {
		fmt.Fprintf(warn, "! another server listens on [::1]:%d: use http://127.0.0.1:%d, not localhost (or pick another --port)\n", port, port)
	}
	return nil
}
