package hostd

import (
	"fmt"
	"slices"
	"strings"
)

// seatbeltExecDirs are the system dirs a sandboxed workload may exec from
// (a Node app's child_process: /bin/sh, /usr/bin/…), besides its runtime
// binary, a Node install prefix and its own dirs. The Linux sandbox allows
// the same OS dirs (procsb's systemRO).
var seatbeltExecDirs = []string{
	"/System",
	"/bin",
	"/opt",
	"/sbin",
	"/usr",
}

// seatbeltReadDirs are the system dirs a sandboxed workload may read,
// besides its runtime binary, a Node install prefix and its own dirs
// (issue #175): the OS and its dyld shared cache (/System, /usr, /bin,
// /sbin), /etc (CA bundle, hosts, localtime), the time zone database
// /etc/localtime points into, /bin/sh's /var/select link, devices, and /opt
// (Homebrew: node's dylibs). The Linux sandbox allows the same (procsb's
// systemRO). Not /Library, /Applications, /Users, /tmp, /var/folders or
// the rest of /var.
var seatbeltReadDirs = []string{
	"/System",
	"/bin",
	"/dev",
	"/opt",
	"/private/etc",
	"/private/var/db/timezone",
	"/private/var/select",
	"/sbin",
	"/usr",
}

// seatbeltRootLinks are stat-able (not readable) under the strict profile:
// the root symlinks into /private, which a process resolves before it
// opens /etc/hosts or a /var/folders path. "/" itself is readable (every
// process opens it at startup; it lists the same names on every Mac).
var seatbeltRootLinks = []string{
	"/etc",
	"/tmp",
	"/var",
}

// seatbeltDevWrite are the device files a workload may open for writing
// (dyld registers with /dev/dtracehelper at startup).
var seatbeltDevWrite = []string{
	"/dev/dtracehelper",
	"/dev/null",
	"/dev/zero",
}

// seatbeltProfile is the Seatbelt profile of a process-tier workload
// (issues #121, #175). On top of (allow default), which keeps the Mach
// services and sysctls workerd and node need:
//
//   - reads are an allow list: the OS dirs (seatbeltReadDirs), each of
//     readOnly (the runtime binary), everything below readOnlyTrees (a Node
//     install prefix) and own. Each of deny (home dir, hostd's data and run
//     dirs, credential files) is denied even inside an allowed OS dir (a
//     data dir under /opt) and allowed again only under own. Outside the
//     allow list only the metadata (stat) of "/" and of the allowed paths'
//     parent dirs is readable, so that dyld and node can resolve paths;
//   - writes are denied everywhere except own and a few devices;
//   - all network is denied (no DNS: netd resolves names), except unix
//     sockets in own, IPv4 TCP connects to localhost:<connectTCP> (a Node
//     app's egress proxy and its own port) and IPv4 TCP binds on
//     localhost:<bindTCP> (its own port, workerd's inspector). Seatbelt
//     takes no literal address ("host must be * or localhost"); tcp4 pins
//     the family, so [::1]:<port> is refused, and 127.0.0.1, where hostd
//     listens, is the only IPv4 loopback address macOS configures;
//   - signals reach only processes in the same sandbox instance (the
//     workload itself and its children): not tw-hostd, tw-mgmt or another
//     workload, even of the same tenant;
//   - exec is allowed only of the runtime binary, the Node prefix, own and
//     the OS dirs (seatbeltExecDirs).
//
// Seatbelt applies the last matching rule, so allows follow denies.
func seatbeltProfile(own, readOnly, readOnlyTrees, deny []string, bindTCP, connectTCP []uint16) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny file-read*)\n(allow file-read* (literal \"/\"))\n")
	for _, d := range seatbeltReadDirs {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", sbQuote(d))
	}
	b.WriteString("(deny file-write*)\n")
	for _, d := range seatbeltDevWrite {
		fmt.Fprintf(&b, "(allow file-write* (literal %s))\n", sbQuote(d))
	}
	b.WriteString("(deny network*)\n")
	for _, p := range connectTCP {
		fmt.Fprintf(&b, "(allow network-outbound (remote tcp4 \"localhost:%d\"))\n", p)
	}
	for _, p := range bindTCP {
		fmt.Fprintf(&b, "(allow network-bind network-inbound (local tcp4 \"localhost:%d\"))\n", p)
	}
	b.WriteString("(deny signal)\n(allow signal (target same-sandbox))\n")
	b.WriteString("(deny process-exec*)\n")
	for _, d := range seatbeltExecDirs {
		fmt.Fprintf(&b, "(allow process-exec (subpath %s))\n", sbQuote(d))
	}
	for _, o := range readOnly {
		for _, p := range []string{o, realPath(o)} {
			fmt.Fprintf(&b, "(allow process-exec (literal %s))\n", sbQuote(p))
		}
	}
	for _, o := range append(append([]string{}, readOnlyTrees...), own...) {
		fmt.Fprintf(&b, "(allow process-exec (subpath %s))\n", sbQuote(realPath(o)))
	}
	for _, r := range realPaths(deny) {
		fmt.Fprintf(&b, "(deny file-read* file-write* (subpath %s))\n", sbQuote(r))
	}
	for _, m := range seatbeltRootLinks {
		fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", sbQuote(m))
	}
	for _, m := range ancestors(slices.Concat(seatbeltReadDirs, own, readOnlyTrees, readOnly)) {
		fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", sbQuote(m))
	}
	for _, o := range readOnly {
		for _, p := range []string{o, realPath(o)} {
			fmt.Fprintf(&b, "(allow file-read* (literal %s))\n", sbQuote(p))
		}
	}
	for _, o := range readOnlyTrees {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", sbQuote(realPath(o)))
	}
	for _, o := range own {
		r := realPath(o)
		fmt.Fprintf(&b, "(allow file-read* file-write* (subpath %s))\n", sbQuote(r))
		fmt.Fprintf(&b, "(allow network-outbound (remote unix-socket (subpath %s)))\n", sbQuote(r))
		fmt.Fprintf(&b, "(allow network-bind network-inbound (local unix-socket (subpath %s)))\n", sbQuote(r))
	}
	return b.String()
}
