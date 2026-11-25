package mgmtd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
)

// The single-use join key handoff (Options.TSNetAuthKeyOnce). `tw up`
// mints mgmt's first join key with the tenant's credential and writes it
// to a 0600 file in mgmt's 0700 state dir, instead of joining the node
// itself and starting tsnet twice (#245). mgmt reads the key and deletes
// the file before it joins; a Start that fails before that deletes it
// too. The key is single use, pre-authorized, tagged and expires within
// 5 minutes either way. hostd's join file (tw-hostd -join-token-file)
// works the same way.

// StartErrFile is where a Start with TSNetAuthKeyOnce records why it
// failed (0600, redacted), so `tw up` can report the join error itself
// instead of "mgmt did not become healthy". The first failure wins: a
// restart that fails again doesn't overwrite it, and a successful Start
// removes it.
func StartErrFile(stateDir string) string { return filepath.Join(stateDir, "start.err") }

// readJoinKeyOnce reads TS_AUTHKEY from the handoff file and deletes the
// file. A missing file is no key (the node's saved state is used); a file
// group or others can read is refused (and deleted: it is single use).
func readJoinKeyOnce(path string) (string, error) {
	k, err := tsapi.LoadSecretEnvFile(path, "TS_AUTHKEY")
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	os.Remove(path)
	if err != nil {
		return "", err
	}
	return k, nil
}

// recordStartErr writes err to StartErrFile unless an earlier failure is
// already there. Written whole, then linked into place: a reader never
// sees it half written, and link refuses to replace an earlier one.
func recordStartErr(stateDir string, err error) {
	f, ferr := os.CreateTemp(stateDir, ".start.err-*")
	if ferr != nil {
		return
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(strings.TrimSpace(netd.Redact(err.Error())) + "\n")
	if f.Close() != nil || werr != nil {
		return
	}
	os.Link(f.Name(), StartErrFile(stateDir))
}
