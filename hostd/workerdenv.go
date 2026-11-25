package hostd

import (
	"os"

	"github.com/rosskukulinski/tailworks/internal/wallclock"
)

// WorkerdEnv is the environment a process-tier workerd runs with (#336).
// It is built from nothing, as the Node tier's is (spawnNode in node.go),
// not inherited from tw-hostd: under a foreground `tw up` hostd's
// environment is the operator's shell (cloud credentials, API tokens,
// SSH_AUTH_SOCK), under `--service` the service manager's, and code that
// escapes V8 can read its own process's environment (on Linux
// /proc/self/environ is on the sandbox's read list) and send it out over the
// egress that is on by default. Every branch of sandboxed() (Landlock
// through the procsb launcher, sandbox-exec, none) gets this list, and
// `tw doctor`'s workerd probe runs with it too.
//
// What workerd reads was measured with `strings` on the binary (the names
// it and the libraries it links, ICU, V8, KJ, OpenSSL, know): TZ (time
// zone), LANG/LC_ALL/LC_MESSAGES (ICU's default locale, which V8 maps to
// en-US when they are unset, so leaving them out gives every workload the
// same default, as Workers have), TMPDIR/TMP (KJ temp files; both sandboxes
// refuse /tmp and workerd runs without), NO_COLOR (log colour), USER and PWD,
// its own WORKERD_* knobs and ICU_DATA. Only TZ is passed, and only when
// hostd has it, so a workload keeps the host's time zone as before. No HOME,
// no PATH (hostd runs workerd by path, and workerd execs nothing), no
// inherited variable. The microVM guest's workerd gets a fixed list of the
// same kind from tw-guestd (`HOME=/tmp PATH=/usr/bin:/bin TZ=UTC`).
func WorkerdEnv() []string {
	env := []string{} // not nil: nil makes exec.Cmd inherit hostd's environment
	for _, k := range []string{
		"TZ",
	} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// A test build's skewed clock (internal/wallclock); nothing otherwise.
	wenv, _ := wallclock.Workerd()
	return append(env, wenv...)
}
