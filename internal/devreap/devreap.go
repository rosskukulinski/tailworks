// Package devreap cleans up after a tw dev (or test) process that died
// without running its defers, SIGKILL included: the workerd and node
// children it started, and its private temp dir.
//
// One mechanism on every OS: the first Guard starts a small watchdog, a
// re-exec of the current binary in its own session that holds the read end
// of a pipe. Only this process holds the write end, so the watchdog reads
// EOF when this process exits, however it exits. The watchdog then kills the
// processes that belong to each guarded dir and removes the dir.
//
// A process belongs to a dir when it runs as this user, its program is one
// of the dir's programs (workerd and node, plus the ones Guard names), and
// its command line names a path inside the dir. Every workerd hostd starts
// passes its config under RunDir; node runs with a preload from the
// instance dir. Nothing else is ever signalled.
//
// Guard also records the owner's pid in the dir (OwnerFile), so ReapStale
// can clean up dirs whose owner and watchdog are both gone (a reboot, or a
// kill of the whole session) the next time tw dev starts. A dir whose
// owner is alive, such as another worktree's tw dev, is left alone.
package devreap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// WatchArg is the watchdog's argv[1].
const WatchArg = "__tw-devreap"

// OwnerFile is the file in a guarded dir that records its owner.
const OwnerFile = ".tailworks-owner.json"

// defaultPrograms are always a dir's programs.
var defaultPrograms = []string{"workerd", "node"}

// Options for Guard.
type Options struct {
	// Programs are more programs (paths or base names) whose processes may
	// belong to the dir, beyond workerd and node.
	Programs []string
	// Keep leaves the dir in place: only its processes are killed.
	Keep bool
}

type owner struct {
	PID      int      `json:"pid"`
	Programs []string `json:"programs"`
	Keep     bool     `json:"keep,omitempty"`
}

// The watchdog runs from package init, before main or testing.Main, in any
// binary that imports devreap (tailworks, and the test binaries of the packages
// that use it).
func init() {
	if len(os.Args) > 1 && os.Args[1] == WatchArg {
		os.Exit(watchMain())
	}
}

var (
	mu    sync.Mutex
	watch *os.File // write end of the watchdog's pipe
)

// Guard records this process as dir's owner and makes sure dir's processes
// are killed and dir removed (unless o.Keep) when this process exits, even
// by SIGKILL. dir must be a private dir this process created.
func Guard(dir string, o Options) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(owner{PID: os.Getpid(), Programs: o.Programs, Keep: o.Keep})
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), b, 0o600); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if watch == nil {
		if watch, err = startWatchdog(); err != nil {
			return err
		}
	}
	// One line per dir, well under PIPE_BUF: the write is atomic.
	_, err = fmt.Fprintln(watch, dir)
	return err
}

func startWatchdog() (*os.File, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe() // close-on-exec: no other child inherits w
	if err != nil {
		return nil, err
	}
	defer r.Close()
	cmd := exec.Command(exe, WatchArg)
	cmd.ExtraFiles = []*os.File{r} // fd 3
	cmd.Dir = "/"
	// Its own session: a kill of this process's group (a harness timeout)
	// doesn't take the watchdog with it. stdio is /dev/null, so it never
	// holds a caller's output pipe open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		return nil, err
	}
	go cmd.Wait()
	return w, nil
}

// watchMain is the watchdog: collect dirs from fd 3 until EOF (the owner
// has exited), then reap them.
func watchMain() int {
	f := os.NewFile(3, "devreap")
	if f == nil {
		return 2
	}
	var dirs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if d := sc.Text(); filepath.IsAbs(d) && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		Reap(d)
	}
	return 0
}

// ReapStale reaps every dir matching glob whose recorded owner has exited.
// Dirs without an owner file, owned by another user, or whose owner is
// still running are left alone. It returns the dirs it reaped.
func ReapStale(glob string) []string {
	ms, _ := filepath.Glob(glob)
	var out []string
	for _, d := range ms {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || !ownedByMe(fi) {
			continue
		}
		o, err := readOwner(d)
		if err != nil || o.PID <= 0 || o.PID == os.Getpid() || alive(o.PID) {
			continue
		}
		Reap(d)
		out = append(out, d)
	}
	return out
}

// Reap kills dir's processes (SIGTERM, then SIGKILL after a grace period)
// and removes dir unless its owner file says to keep it. A dir without an
// owner file is not touched.
func Reap(dir string) {
	o, err := readOwner(dir)
	if err != nil {
		return
	}
	pids := Processes(dir, o.Programs...)
	signal(pids, syscall.SIGTERM)
	dl := time.Now().Add(3 * time.Second)
	for time.Now().Before(dl) && anyAlive(pids) {
		time.Sleep(50 * time.Millisecond)
	}
	signal(pids, syscall.SIGKILL)
	if !o.Keep {
		os.RemoveAll(dir)
	}
}

// Processes lists the pids that belong to dir: this user's processes whose
// program is workerd, node or one of programs, and whose command line names
// a path inside dir.
func Processes(dir string, programs ...string) []int {
	names := map[string]bool{}
	for _, p := range append(append([]string{}, defaultPrograms...), programs...) {
		if p != "" {
			names[filepath.Base(p)] = true
		}
	}
	roots := []string{filepath.Clean(dir) + "/"}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r+"/" != roots[0] {
		roots = append(roots, r+"/")
	}
	procs, err := listProcs()
	if err != nil {
		return nil
	}
	inside := func(s string) bool {
		for _, r := range roots {
			if strings.Contains(s, r) {
				return true
			}
		}
		return false
	}
	me, self := os.Getuid(), os.Getpid()
	var out, rest []int
	for _, p := range procs {
		if p.uid != me || p.pid == self || p.pid <= 1 || len(p.args) == 0 {
			continue
		}
		if !names[filepath.Base(p.args[0])] {
			continue
		}
		if inside(strings.Join(p.args[1:], " ")) {
			out = append(out, p.pid)
		} else {
			rest = append(rest, p.pid)
		}
	}
	// A node app's command line can be all relative (its main, no
	// preload): it belongs to dir if it runs in dir.
	for pid, cwd := range cwds(rest) {
		for _, r := range roots {
			if strings.HasPrefix(cwd+"/", r) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

type proc struct {
	pid, uid int
	args     []string
}

// listProcs runs ps (the same flags on macOS and Linux procps).
func listProcs() ([]proc, error) {
	b, err := exec.Command("ps", "-A", "-ww", "-o", "pid=", "-o", "uid=", "-o", "args=").Output()
	if err != nil {
		return nil, err
	}
	var out []proc
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		uid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, proc{pid: pid, uid: uid, args: f[2:]})
	}
	return out, nil
}

// signal sends sig to each pid's process group when it leads one (hostd
// starts workerd and node with Setpgid, so this also takes whatever a node
// app spawned), else to the pid alone.
func signal(pids []int, sig syscall.Signal) {
	for _, p := range pids {
		if g, err := syscall.Getpgid(p); err == nil && g == p {
			syscall.Kill(-p, sig)
		} else {
			syscall.Kill(p, sig)
		}
	}
}

func anyAlive(pids []int) bool {
	for _, p := range pids {
		if alive(p) {
			return true
		}
	}
	return false
}

// alive reports whether pid exists (EPERM: it does, as another user).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

func readOwner(dir string) (owner, error) {
	var o owner
	b, err := os.ReadFile(filepath.Join(dir, OwnerFile))
	if err != nil {
		return o, err
	}
	return o, json.Unmarshal(b, &o)
}
