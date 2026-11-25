// Package r2 is the blob backend of R2 buckets (ADR-036).
//
// An R2 bucket is a platform store of kind "r2": its index (key → version,
// size, etag, metadata, blob IDs) is a platform store, journaled to mgmt
// (ADR-044). The object bytes are immutable blobs in a Backend
// that the host's operator configures: a directory (FS, the default) or an
// S3-compatible service (S3). Only tw-hostd talks to the backend, so the
// workload never holds a credential (ADR-034).
//
// Blob names are built by Name from the tenant, the workload and the
// bucket of the calling instance (as hostd knows them, never from the
// request) and a blob ID that hostd generated, so no request a workload
// can cause names another workload's or tenant's data. A workload's
// replicas ("<workload>--r<n>") are the one case of two workload names
// with one prefix: they share its buckets. No other workload can be given
// such a name (mgmt refuses it, internal/replicaname, #348).
package r2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Backend stores immutable blobs by name.
type Backend interface {
	// Put stores exactly size bytes read from r as name (replacing nothing:
	// names are unique). It fails, storing nothing, if r ends early.
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	// Get reads name from byte off; n < 0 reads to the end. ErrNotFound if
	// there is no such blob.
	Get(ctx context.Context, name string, off, n int64) (io.ReadCloser, error)
	// Delete removes name; a missing blob is not an error.
	Delete(ctx context.Context, name string) error
	// String describes the backend for logs (never a credential).
	String() string
}

// ErrNotFound is a blob that isn't there.
var ErrNotFound = errors.New("r2: blob not found")

// MaxBlob is the largest blob (R2's object limit: 5 GiB − 5 MiB).
const MaxBlob = 5<<30 - 5<<20

var (
	segRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	blobIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// NewID returns a new random blob ID (32 hex characters).
func NewID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ValidID reports whether id is a blob ID NewID could have made.
func ValidID(id string) bool { return blobIDRE.MatchString(id) }

// Name is the backend name of blob id of bucket, for tenant's workload:
// "t/<tenant>/w/<workload>/b/<bucket>/<id>". Every element is checked, so
// none can contain a "/" or be "." or "..".
func Name(tenant, workload, bucket, id string) (string, error) {
	for _, s := range []string{tenant, workload, bucket} {
		if !segRE.MatchString(s) || strings.Contains(s, "..") {
			return "", fmt.Errorf("r2: bad name element %q", s)
		}
	}
	if !ValidID(id) {
		return "", fmt.Errorf("r2: bad blob ID %q", id)
	}
	return "t/" + tenant + "/w/" + workload + "/b/" + bucket + "/" + id, nil
}
