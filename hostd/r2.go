package hostd

// R2 buckets (ADR-036), host side. A bucket's index is a platform
// store of kind "r2" (stores.go); its bytes are immutable blobs in the
// host's R2 backend (Config.R2: a directory or an S3-compatible service).
// The platform's R2Backend (hostd/gen/js/store-r2.js) streams them through
// hostd's local host API, which tenant code can't reach:
//
//	PUT    /v1/r2/blob?bucket=B[&hash=SHA-256…]  body: the bytes, with Content-Length
//	       → {"id", "size", "md5", "sha1"?, "sha256"?, "sha384"?, "sha512"?} (hex)
//	GET    /v1/r2/blob?bucket=B&id=X[&off=N][&len=N] → the bytes
//	DELETE /v1/r2/blob?bucket=B&id=X[&id=Y…]          → 204
//
// hostd names the blob itself (r2.Name): the tenant and the workload are
// the calling instance's, the bucket must be an r2_buckets binding of its
// deployment, and the ID one hostd generated. Bodies are copied with a
// fixed buffer; nothing holds a whole object.

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"

	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/internal/replicaname"
)

// r2Group is the workload whose buckets name's blobs belong to: a replica
// ("<workload>--r<n>", n ≥ 2) uses its workload's buckets, as it uses its
// stores.
//
// hostd has only the name to go by here. mgmt resolves a replica's stores
// and secrets from its deployment record (replicaGroupOf), and refuses a
// workload that is not a replica a name shaped like one (checkReplicaName,
// #348), reading names with the same replicaname.Group. So the blob prefix
// and the index scope are the same workload for every name mgmt deploys.
// The exception is a workload deployed under such a name before mgmt had
// that check: it keeps its own index and, as before, shares the blob
// prefix of the workload it is named after, until it is renamed.
func r2Group(name string) string {
	if g, ok := replicaname.Group(name); ok {
		return g
	}
	return name
}

func writeR2Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
}

var r2Hashes = map[string]func() hash.Hash{"SHA-1": sha1.New, "SHA-256": sha256.New, "SHA-384": sha512.New384, "SHA-512": sha512.New}
var r2HashField = map[string]string{"SHA-1": "sha1", "SHA-256": "sha256", "SHA-384": "sha384", "SHA-512": "sha512"}

// r2API serves /v1/r2/blob for inst's platform.
func (h *Host) r2API(inst *instance, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/r2/blob" {
		writeR2Error(w, 404, "bad_request", "unknown r2 route")
		return
	}
	q := r.URL.Query()
	bucket := q.Get("bucket")
	declared := false
	for _, b := range inst.app.Stores {
		if b.Kind == "r2" && b.Name == bucket {
			declared = true
			break
		}
	}
	if !declared {
		writeR2Error(w, 400, "bad_request", "bucket "+strconv.Quote(bucket)+" is not bound by this deployment")
		return
	}
	wl := inst.w
	be := h.cfg.R2
	name := func(id string) (string, bool) {
		n, err := r2.Name(wl.tenantID, r2Group(wl.name), bucket, id)
		if err != nil {
			writeR2Error(w, 400, "bad_request", err.Error())
			return "", false
		}
		return n, true
	}
	ctx := r.Context()
	switch r.Method {
	case "PUT":
		size := r.ContentLength
		if size < 0 {
			writeR2Error(w, 411, "bad_request", "r2 blob: Content-Length is required")
			return
		}
		if size > r2.MaxBlob {
			writeR2Error(w, 400, "r2_too_large", "r2 blob: larger than "+strconv.FormatInt(r2.MaxBlob, 10)+" bytes")
			return
		}
		id := r2.NewID()
		n, ok := name(id)
		if !ok {
			return
		}
		sums := map[string]hash.Hash{"md5": md5.New()}
		for _, a := range q["hash"] {
			f, ok := r2Hashes[a]
			if !ok {
				writeR2Error(w, 400, "bad_request", "r2 blob: unknown hash "+strconv.Quote(a))
				return
			}
			sums[r2HashField[a]] = f()
		}
		ws := make([]io.Writer, 0, len(sums))
		for _, s := range sums {
			ws = append(ws, s)
		}
		if err := be.Put(ctx, n, io.TeeReader(r.Body, io.MultiWriter(ws...)), size); err != nil {
			h.log.Warn("r2: blob put failed", "tenant", wl.tenantID, "workload", wl.name, "bucket", bucket, "err", err)
			writeR2Error(w, 503, "r2_backend_unavailable", "r2 blob put: "+err.Error())
			return
		}
		out := map[string]any{"id": id, "size": size}
		for k, s := range sums {
			out[k] = hex.EncodeToString(s.Sum(nil))
		}
		writeJSON(w, 200, out)
	case "GET":
		id := q.Get("id")
		n, ok := name(id)
		if !ok {
			return
		}
		off, err1 := strconv.ParseInt(q.Get("off"), 10, 64)
		if q.Get("off") == "" {
			off, err1 = 0, nil
		}
		ln := int64(-1)
		var err2 error
		if q.Get("len") != "" {
			ln, err2 = strconv.ParseInt(q.Get("len"), 10, 64)
		}
		if err1 != nil || err2 != nil || off < 0 || ln < -1 {
			writeR2Error(w, 400, "bad_request", "r2 blob: bad off or len")
			return
		}
		rc, err := be.Get(ctx, n, off, ln)
		if errors.Is(err, r2.ErrNotFound) {
			writeR2Error(w, 404, "r2_blob_missing", "r2 blob "+id+" of bucket "+bucket+" is not in this host's R2 backend ("+be.String()+")")
			return
		}
		if err != nil {
			h.log.Warn("r2: blob get failed", "tenant", wl.tenantID, "workload", wl.name, "bucket", bucket, "err", err)
			writeR2Error(w, 503, "r2_backend_unavailable", "r2 blob get: "+err.Error())
			return
		}
		defer rc.Close()
		w.Header().Set("content-type", "application/octet-stream")
		if ln >= 0 {
			w.Header().Set("content-length", strconv.FormatInt(ln, 10))
		}
		w.WriteHeader(200)
		if n, err := io.Copy(w, rc); err != nil || (ln >= 0 && n != ln) {
			// Cut the response so the platform sees a broken body, not a
			// short object.
			panic(http.ErrAbortHandler)
		}
	case "DELETE":
		for _, id := range q["id"] {
			n, ok := name(id)
			if !ok {
				return
			}
			if err := be.Delete(ctx, n); err != nil {
				h.log.Warn("r2: blob delete failed", "tenant", wl.tenantID, "workload", wl.name, "bucket", bucket, "err", err)
				writeR2Error(w, 503, "r2_backend_unavailable", "r2 blob delete: "+err.Error())
				return
			}
		}
		w.WriteHeader(204)
	default:
		writeR2Error(w, 405, "bad_request", "r2 blob: method not allowed")
	}
}
