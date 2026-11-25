//go:build linux

package procsb

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// The launcher runs from package init, before main or testing.Main, in any
// binary that imports procsb (tw-hostd, tailworks, hostd's test binaries).
func init() {
	if len(os.Args) > 1 && os.Args[1] == ExecArg {
		os.Exit(launcherMain(os.Args[2:]))
	}
}

// systemRO are read-and-execute everywhere (if present): the OS, its
// libraries, CA certificates, time zones. They hold nothing of tailworks'.
var systemRO = []string{"/usr", "/lib", "/lib64", "/lib32", "/bin", "/sbin", "/etc", "/opt", "/nix/store", "/run/current-system"}

// procRO and sysRO are the global /proc and /sys files runtimes read (CPU
// count, memory, cgroup limits). Not /proc itself: other processes' cmdline,
// environ (the Node tier's caller key) and /proc/net/unix (every socket's
// path) stay unreadable.
var procRO = []string{"/proc/cpuinfo", "/proc/meminfo", "/proc/stat", "/proc/loadavg", "/proc/uptime",
	"/proc/version", "/proc/version_signature", "/proc/filesystems", "/proc/sys/kernel", "/proc/sys/vm", "/proc/sys/fs",
	"/proc/sys/net/core/somaxconn"}
var sysRO = []string{"/sys/devices/system/cpu", "/sys/fs/cgroup"}

// selfProc are the process's own /proc entries (by pid: exec keeps it).
// Not task/ or net/ (net/unix lists every unix socket's path).
var selfProc = []string{"maps", "smaps", "smaps_rollup", "stat", "statm", "status", "cgroup", "limits", "mountinfo",
	"mounts", "auxv", "cmdline", "comm", "environ", "io", "fd", "fdinfo", "oom_score_adj"}

// Rules builds the Landlock rules for p, for the process with pid.
// abi is the kernel's Landlock ABI: "refer" is only requested on ABI >= 2.
func (p *Profile) Rules(pid, abi int) []landlock.Rule {
	ro := func(paths []string) []landlock.Rule {
		var rs []landlock.Rule
		for _, x := range paths {
			st, err := os.Stat(x)
			if err != nil {
				continue
			}
			if st.IsDir() {
				rs = append(rs, landlock.RODirs(x))
			} else {
				rs = append(rs, landlock.ROFiles(x))
			}
		}
		return rs
	}
	var rs []landlock.Rule
	rs = append(rs, ro(systemRO)...)
	rs = append(rs, ro(p.Exec)...)
	rs = append(rs, ro(p.ReadOnly)...)
	rs = append(rs, ro(procRO)...)
	rs = append(rs, ro(sysRO)...)
	var self []string
	for _, e := range selfProc {
		self = append(self, "/proc/"+strconv.Itoa(pid)+"/"+e)
	}
	rs = append(rs, ro(self)...)
	rs = append(rs,
		landlock.RWFiles("/dev/null", "/dev/zero", "/dev/full").IgnoreIfMissing(),
		landlock.ROFiles("/dev/urandom", "/dev/random").IgnoreIfMissing(),
	)
	if len(p.Own) > 0 {
		rs = append(rs, ownRule(p.Own, abi))
	}
	for _, port := range p.ConnectTCP {
		rs = append(rs, landlock.ConnectTCP(port))
	}
	for _, port := range p.BindTCP {
		rs = append(rs, landlock.BindTCP(port))
	}
	return rs
}

// ownRule is read-write on the workload's own dirs. "refer" (moving and
// linking files between directories) exists from Landlock ABI 2 only, and
// go-landlock's BestEffort mode drops the WHOLE ruleset ("ABI V0": no
// filesystem restriction at all) when a rule asks for refer on an ABI 1
// kernel such as Ubuntu 22.04's 5.15. So ask for it only on ABI >= 2. On
// ABI 1, Landlock refuses cross-directory renames and links (EXDEV) inside
// the own dirs, which beats no sandbox.
func ownRule(own []string, abi int) landlock.Rule {
	r := landlock.RWDirs(own...).WithResolveUnix()
	if abi >= 2 {
		r = r.WithRefer()
	}
	return r
}

// Apply restricts the calling process (all threads) to p: Landlock (best
// effort up to the newest ABI go-landlock knows), then no_new_privs and
// the seccomp filter. It fails if the kernel has no Landlock at all.
func (p *Profile) Apply() error {
	abi, err := llsys.LandlockGetABIVersion()
	if err != nil || abi < 1 {
		return fmt.Errorf("landlock unavailable: %v", err)
	}
	if err := p.checkLayout(); err != nil { // layout_linux.go (#342)
		return err
	}
	for _, o := range p.Own {
		if err := os.MkdirAll(o, 0o700); err != nil {
			return err
		}
	}
	if err := landlock.V11.BestEffort().Restrict(p.Rules(os.Getpid(), abi)...); err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	return installSeccomp(filterOpts{self: os.Getpid(), noUnix: p.NoUnixSockets, noListen: p.NoListen})
}

func launcherMain(args []string) int {
	runtime.LockOSThread()
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "tw sandbox: "+format+"\n", a...)
		return 126
	}
	if len(args) < 3 || args[0] != "-f" {
		return fail("usage: %s -f <profile.json> (-- <bin> [args…] | --probe <op> [args…])", ExecArg)
	}
	p, err := Load(args[1])
	if err != nil {
		return fail("%v", err)
	}
	switch args[2] {
	case "--":
		if len(args) < 4 {
			return fail("no command")
		}
		bin, err := exec.LookPath(args[3])
		if err != nil {
			return fail("%v", err)
		}
		if bin, err = filepath.Abs(bin); err != nil {
			return fail("%v", err)
		}
		if err := p.Apply(); err != nil {
			return fail("%v", err)
		}
		// The child gets the environment this launcher was started with and
		// nothing else: Command starts the launcher with an empty one and
		// its caller adds what the workload needs (#336). The launcher
		// itself sets no variable, so there is nothing of its own to drop,
		// and it can't hold an allow list of its own: a Node app's list
		// (TW_CALLER_KEY, the proxy variables, node.env) is the caller's.
		err = syscall.Exec(bin, args[3:], os.Environ())
		return fail("exec %s: %v", bin, err)
	case "--probe":
		if len(args) < 4 {
			return fail("no probe op")
		}
		if err := p.Apply(); err != nil {
			return fail("%v", err)
		}
		err := probe(args[3], args[4:])
		if err == nil {
			fmt.Println("ok")
			return 0
		}
		var en syscall.Errno
		if errors.As(err, &en) {
			fmt.Println(errnoName(en))
		} else {
			fmt.Println(err)
		}
		return 3
	}
	return fail("unknown mode %q", args[2])
}
