package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// GuestIdentity identifies the guest a VM boots: the SHA-256 of the
// image's manifest.json (it records the rootfs, guestd and workerd hashes
// and, from tw_version on, the release; guest/build-image.sh), plus the
// size and mtime of the kernel and rootfs files themselves (a hand-copied
// image without a fresh manifest still changes identity).
//
// ADR-030 base snapshots are keyed by it (ADR-041 §5):
// a base is never restored under a different guest, e.g. after install.sh
// swapped share/tailworks/vm under a running hostd. Restores then boot cold
// until the next base is built from the new guest.
func GuestIdentity(kernel, rootfs string) (string, error) {
	h := sha256.New()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(rootfs), "manifest.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		h.Write([]byte("no-manifest\n"))
	case err != nil:
		return "", err
	default:
		h.Write(b)
	}
	for _, p := range []string{kernel, rootfs} {
		fi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "\n%s %d %d", filepath.Base(p), fi.Size(), fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
