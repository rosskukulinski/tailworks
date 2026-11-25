package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestDropLines(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	d := dropLines{w: &b, drop: [][]byte{[]byte("STUN server listening on ")}}
	d.Write([]byte("2026/11/14 STUN server listening on 127.0.0.1:1\n"))
	d.Write([]byte("something real\n"))
	if b.String() != "something real\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestCheckDevPort(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	err = checkDevPort(port, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("busy port: %v", err)
	}
	ln.Close()
	// The cases below need 127.0.0.1:port free, and parallel tests hold
	// loopback ports: one can take a port we just closed, and the port the
	// kernel picks for [::1]:0 can already be in use on 127.0.0.1 (a CI
	// flake). So a 127.0.0.1 clash means "try another port", not a failure.
	if _, err := checkFreeOn4(t, "127.0.0.1:0"); err != nil {
		t.Fatalf("free port: %v", err)
	}
	// Taken on ::1 only: a warning, not an error.
	if ln6, err := net.Listen("tcp", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback")
	} else {
		ln6.Close()
	}
	w, err := checkFreeOn4(t, "[::1]:0")
	if err != nil {
		t.Fatalf("::1 only: %v", err)
	}
	if !strings.Contains(w, "[::1]") {
		t.Fatalf("no ::1 warning: %q", w)
	}
}

// checkFreeOn4 runs checkDevPort on a port the kernel gives for addr
// (held on addr during the check when it is [::1], released first when it
// is 127.0.0.1), with a fresh port while the check finds 127.0.0.1 taken
// by someone else. It returns the warnings and the last error.
func checkFreeOn4(t *testing.T, addr string) (string, error) {
	t.Helper()
	var err error
	for try := 0; try < 50; try++ {
		ln, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			t.Fatal(lerr)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if strings.HasPrefix(addr, "127.") {
			ln.Close()
		}
		var w bytes.Buffer
		err = checkDevPort(port, &w)
		ln.Close()
		if err == nil || !strings.Contains(err.Error(), "in use on 127.0.0.1") {
			return w.String(), err
		}
	}
	return "", err
}
