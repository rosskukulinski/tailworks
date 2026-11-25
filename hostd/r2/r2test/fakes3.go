// Package r2test has a hermetic S3-compatible server for tests of the R2
// blob backend (hostd/r2).
package r2test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/rosskukulinski/tailworks/hostd/r2"
)

// FakeS3 is an S3 server for one bucket, path style, keeping objects as
// files (so a test that measures memory isn't measuring the fake). It
// checks every request's SigV4 signature and serves PutObject (with
// Content-Length), GetObject (one Range) and DeleteObject.
type FakeS3 struct {
	URL    string
	Bucket string
	Creds  r2.Credentials
	Region string
	Dir    string

	srv *httptest.Server

	Puts, Gets, Deletes, Denied atomic.Int64
}

// StartFakeS3 starts a FakeS3 storing under dir.
func StartFakeS3(dir string) *FakeS3 {
	f := &FakeS3{Bucket: "tw-test", Region: "us-east-1", Dir: dir,
		Creds: r2.Credentials{AccessKeyID: "AKIDTWTEST", SecretAccessKey: "tw-test-secret"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	return f
}

// Close stops the server.
func (f *FakeS3) Close() { f.srv.Close() }

// Backend is an r2.S3 pointed at f.
func (f *FakeS3) Backend(prefix string) *r2.S3 {
	u, _ := url.Parse(f.URL)
	return &r2.S3{Endpoint: u, Bucket: f.Bucket, Region: f.Region, Prefix: prefix, Creds: f.Creds}
}

// Keys are the object keys stored (sorted by the filesystem walk).
func (f *FakeS3) Keys() []string {
	var out []string
	filepath.WalkDir(f.Dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.Contains(filepath.Base(p), ".part-") {
			r, _ := filepath.Rel(f.Dir, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	return out
}

func s3err(w http.ResponseWriter, status int, code string) {
	w.Header().Set("content-type", "application/xml")
	w.WriteHeader(status)
	io.WriteString(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>"+code+"</Code><Message>"+code+"</Message></Error>")
}

func (f *FakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if err := r2.VerifyV4(r, f.Creds, f.Region); err != nil {
		f.Denied.Add(1)
		s3err(w, 403, "SignatureDoesNotMatch")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != f.Bucket {
		s3err(w, 404, "NoSuchBucket")
		return
	}
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		s3err(w, 400, "InvalidArgument")
		return
	}
	p := filepath.Join(f.Dir, filepath.FromSlash(key))
	switch r.Method {
	case "PUT":
		f.Puts.Add(1)
		if r.ContentLength < 0 {
			s3err(w, 411, "MissingContentLength")
			return
		}
		if h := r.Header.Get("x-amz-content-sha256"); h != "UNSIGNED-PAYLOAD" && !(r.ContentLength == 0 && h == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") {
			s3err(w, 400, "XAmzContentSHA256Mismatch")
			return
		}
		os.MkdirAll(filepath.Dir(p), 0o700)
		tmp, err := os.CreateTemp(filepath.Dir(p), ".part-*")
		if err != nil {
			s3err(w, 500, "InternalError")
			return
		}
		n, err := io.Copy(tmp, r.Body)
		tmp.Close()
		if err != nil || n != r.ContentLength {
			os.Remove(tmp.Name())
			s3err(w, 400, "IncompleteBody")
			return
		}
		os.Rename(tmp.Name(), p)
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(200)
	case "GET":
		f.Gets.Add(1)
		fh, err := os.Open(p)
		if err != nil {
			s3err(w, 404, "NoSuchKey")
			return
		}
		defer fh.Close()
		st, _ := fh.Stat()
		size := st.Size()
		rg := r.Header.Get("Range")
		if rg == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(200)
			io.Copy(w, fh)
			return
		}
		spec, ok := strings.CutPrefix(rg, "bytes=")
		a, b, ok2 := strings.Cut(spec, "-")
		start, err1 := strconv.ParseInt(a, 10, 64)
		end := size - 1
		var err2 error
		if b != "" {
			end, err2 = strconv.ParseInt(b, 10, 64)
		}
		if !ok || !ok2 || err1 != nil || err2 != nil || start >= size || end < start {
			s3err(w, 416, "InvalidRange")
			return
		}
		if end >= size {
			end = size - 1
		}
		fh.Seek(start, io.SeekStart)
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
		w.WriteHeader(206)
		io.CopyN(w, fh, end-start+1)
	case "DELETE":
		f.Deletes.Add(1)
		os.Remove(p)
		w.WriteHeader(204)
	default:
		s3err(w, 405, "MethodNotAllowed")
	}
}
