package r2

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// S3 keeps blobs as objects of one bucket of an S3-compatible service
// (MinIO, Garage, SeaweedFS, AWS S3, Cloudflare R2, …), under Prefix. It
// makes three calls: PutObject (one request with Content-Length and an
// unsigned payload, so the body streams), GetObject with a Range, and
// DeleteObject. Requests are signed with SigV4 (header auth).
type S3 struct {
	// Endpoint is the service's base URL (https://s3.us-east-1.amazonaws.com,
	// http://minio.lan:9000, …).
	Endpoint *url.URL
	Bucket   string
	Region   string // default "us-east-1" (most S3-compatible servers accept it)
	// Prefix is prepended to every blob name ("tw/" keeps tailworks' blobs in
	// one folder of a shared bucket).
	Prefix string
	// VirtualHost addresses the bucket as <bucket>.<endpoint host> instead
	// of <endpoint>/<bucket> (path style, the default: MinIO, Garage and
	// SeaweedFS need it).
	VirtualHost bool
	Creds       Credentials
	// Client is the HTTP client (default: one without a total timeout, so
	// large bodies can stream).
	Client *http.Client
	// Now is the signing clock (tests).
	Now func() time.Time
}

func (s *S3) String() string {
	return "s3:" + s.Endpoint.Redacted() + "/" + s.Bucket + "/" + s.Prefix
}

func (s *S3) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return defaultS3Client
}

var defaultS3Client = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
}}

func (s *S3) objectURL(name string) *url.URL {
	u := *s.Endpoint
	base := strings.TrimSuffix(u.Path, "/")
	key := s.Prefix + name
	if s.VirtualHost {
		u.Host = s.Bucket + "." + u.Host
		u.Path = base + "/" + key
	} else {
		u.Path = base + "/" + s.Bucket + "/" + key
	}
	u.RawPath = ""
	u.RawQuery = ""
	return &u
}

func (s *S3) do(ctx context.Context, method, name string, body io.Reader, size int64, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.objectURL(name).String(), body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	payload := emptySHA256
	if body != nil {
		req.ContentLength = size
		payload = unsignedPayload
		if size == 0 {
			req.Body = http.NoBody
			payload = emptySHA256
		}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	signV4(req, s.Creds, region, "s3", payload, now())
	return s.client().Do(req)
}

// Put implements Backend.
func (s *S3) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if size < 0 || size > MaxBlob {
		return fmt.Errorf("r2: bad blob size %d", size)
	}
	// The transport sends exactly size bytes and fails if r ends early.
	resp, err := s.do(ctx, "PUT", name, io.LimitReader(r, size), size, http.Header{"Content-Type": {"application/octet-stream"}})
	if err != nil {
		return fmt.Errorf("r2: s3 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return s3Error("put", resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// Get implements Backend.
func (s *S3) Get(ctx context.Context, name string, off, n int64) (io.ReadCloser, error) {
	if n == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	hdr := http.Header{}
	switch {
	case n > 0:
		hdr.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(off+n-1, 10))
	case off > 0:
		hdr.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := s.do(ctx, "GET", name, nil, 0, hdr)
	if err != nil {
		return nil, fmt.Errorf("r2: s3 get: %w", err)
	}
	switch resp.StatusCode {
	case 200, 206:
		if resp.StatusCode == 200 && off > 0 {
			resp.Body.Close()
			return nil, errors.New("r2: s3 get: the backend ignored the Range header")
		}
		return resp.Body, nil
	case 404:
		resp.Body.Close()
		return nil, ErrNotFound
	}
	defer resp.Body.Close()
	return nil, s3Error("get", resp)
}

// Delete implements Backend.
func (s *S3) Delete(ctx context.Context, name string) error {
	resp, err := s.do(ctx, "DELETE", name, nil, 0, nil)
	if err != nil {
		return fmt.Errorf("r2: s3 delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != 404 {
		return s3Error("delete", resp)
	}
	return nil
}

func s3Error(op string, resp *http.Response) error {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if xml.Unmarshal(b, &e) == nil && e.Code != "" {
		return fmt.Errorf("r2: s3 %s: %d %s: %s", op, resp.StatusCode, e.Code, e.Message)
	}
	return fmt.Errorf("r2: s3 %s: %s", op, resp.Status)
}
