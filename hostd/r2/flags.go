package r2

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Flags are tw-hostd's R2 backend flags (ADR-036: the backend is
// per host, set by the operator).
type Flags struct {
	Backend     string
	Dir         string
	Endpoint    string
	Bucket      string
	Region      string
	Prefix      string
	VirtualHost bool
	CredsFile   string
}

// Register adds the flags to fs.
func (f *Flags) Register(fs *flag.FlagSet) {
	fs.StringVar(&f.Backend, "r2-backend", "fs", "where R2 bucket bytes live: fs (files under -r2-dir; this host only) | s3 (an S3-compatible service; needed when workloads can move between hosts)")
	fs.StringVar(&f.Dir, "r2-dir", "", "fs backend: blob directory (default <data-dir>/r2)")
	fs.StringVar(&f.Endpoint, "r2-s3-endpoint", "", "s3 backend: service URL, e.g. https://s3.us-east-1.amazonaws.com or http://minio.lan:9000")
	fs.StringVar(&f.Bucket, "r2-s3-bucket", "", "s3 backend: the bucket that holds every tailworks R2 bucket's blobs (one prefix per tenant, workload and bucket)")
	fs.StringVar(&f.Region, "r2-s3-region", "us-east-1", "s3 backend: signing region")
	fs.StringVar(&f.Prefix, "r2-s3-prefix", "tw/", "s3 backend: key prefix for tailworks' blobs")
	fs.BoolVar(&f.VirtualHost, "r2-s3-virtual-host", false, "s3 backend: address the bucket as <bucket>.<endpoint host> (default: path style)")
	fs.StringVar(&f.CredsFile, "r2-s3-credentials-file", "", "s3 backend: KEY=value file with TW_R2_S3_ACCESS_KEY_ID and TW_R2_S3_SECRET_ACCESS_KEY (default: those, or AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN, from the environment)")
}

// Open returns the configured backend. getenv reads the environment.
func (f *Flags) Open(dataDir string, getenv func(string) string) (Backend, error) {
	switch f.Backend {
	case "", "fs":
		d := f.Dir
		if d == "" {
			d = filepath.Join(dataDir, "r2")
		}
		return &FS{Root: d}, nil
	case "s3":
	default:
		return nil, fmt.Errorf("-r2-backend: %q is not fs or s3", f.Backend)
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, errors.New("-r2-s3-endpoint: want http(s)://host[:port][/path], without credentials or a query")
	}
	if f.Bucket == "" {
		return nil, errors.New("-r2-s3-bucket is required with -r2-backend s3")
	}
	env := getenv
	if f.CredsFile != "" {
		m, err := readEnvFile(f.CredsFile)
		if err != nil {
			return nil, fmt.Errorf("-r2-s3-credentials-file: %w", err)
		}
		env = func(k string) string { return m[k] }
	}
	c := Credentials{AccessKeyID: env("TW_R2_S3_ACCESS_KEY_ID"), SecretAccessKey: env("TW_R2_S3_SECRET_ACCESS_KEY"), SessionToken: env("TW_R2_S3_SESSION_TOKEN")}
	if c.AccessKeyID == "" {
		c = Credentials{AccessKeyID: env("AWS_ACCESS_KEY_ID"), SecretAccessKey: env("AWS_SECRET_ACCESS_KEY"), SessionToken: env("AWS_SESSION_TOKEN")}
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, errors.New("-r2-backend s3: no credentials (set TW_R2_S3_ACCESS_KEY_ID and TW_R2_S3_SECRET_ACCESS_KEY, or -r2-s3-credentials-file)")
	}
	if f.Prefix != "" && !strings.HasSuffix(f.Prefix, "/") {
		f.Prefix += "/"
	}
	return &S3{Endpoint: u, Bucket: f.Bucket, Region: f.Region, Prefix: f.Prefix, VirtualHost: f.VirtualHost, Creds: c}, nil
}

func readEnvFile(p string) (map[string]string, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(l, "export "), "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return m, sc.Err()
}
