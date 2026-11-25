package r2_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/r2"
	"github.com/rosskukulinski/tailworks/hostd/r2/r2test"
)

// TestSigV4AWSExample signs AWS's published S3 example ("GET Object",
// Signature Version 4 header auth) and checks the signature.
func TestSigV4AWSExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	now, _ := time.Parse("20060102T150405Z", "20130524T000000Z")
	r2.SignV4ForTest(req, r2.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		"us-east-1", "s3", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", now)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization:\n got %s\nwant %s", got, want)
	}
}

func TestName(t *testing.T) {
	id := r2.NewID()
	if n, err := r2.Name("t-1", "app", "photos", id); err != nil || n != "t/t-1/w/app/b/photos/"+id {
		t.Fatalf("Name: %q %v", n, err)
	}
	for _, c := range [][4]string{
		{"t/1", "app", "b", id}, {"..", "app", "b", id}, {"t", "a..b", "b", id}, {"t", "app", "b/c", id},
		{"t", "app", "", id}, {"t", "app", "b", "../x"}, {"t", "app", "b", strings.ToUpper(id)}, {"t", "app", ".b", id},
	} {
		if _, err := r2.Name(c[0], c[1], c[2], c[3]); err == nil {
			t.Errorf("Name%v: no error", c)
		}
	}
}

// TestBackends runs the same checks against the fs backend and the S3
// backend (on a hermetic, signature-checking fake S3 server).
func TestBackends(t *testing.T) {
	fake := r2test.StartFakeS3(t.TempDir())
	defer fake.Close()
	for _, tc := range []struct {
		name string
		b    r2.Backend
	}{
		{"fs", &r2.FS{Root: t.TempDir()}},
		{"s3", fake.Backend("pfx/")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b := tc.b
			data := make([]byte, 1<<20+17)
			rand.Read(data)
			name, _ := r2.Name("t1", "app", "bkt", r2.NewID())
			if err := b.Put(ctx, name, bytes.NewReader(data), int64(len(data))); err != nil {
				t.Fatal(err)
			}
			read := func(off, n int64) []byte {
				t.Helper()
				rc, err := b.Get(ctx, name, off, n)
				if err != nil {
					t.Fatalf("get %d+%d: %v", off, n, err)
				}
				defer rc.Close()
				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if !bytes.Equal(read(0, -1), data) {
				t.Fatal("whole read differs")
			}
			if !bytes.Equal(read(10, 100), data[10:110]) {
				t.Fatal("ranged read differs")
			}
			if !bytes.Equal(read(int64(len(data))-5, -1), data[len(data)-5:]) {
				t.Fatal("suffix read differs")
			}
			if got := read(3, 0); len(got) != 0 {
				t.Fatal("empty read not empty")
			}
			// A short body stores nothing.
			short, _ := r2.Name("t1", "app", "bkt", r2.NewID())
			if err := b.Put(ctx, short, bytes.NewReader(data[:10]), 11); err == nil {
				t.Fatal("short body accepted")
			}
			if _, err := b.Get(ctx, short, 0, -1); !errors.Is(err, r2.ErrNotFound) {
				t.Fatalf("short body left a blob: %v", err)
			}
			// Empty blobs work.
			empty, _ := r2.Name("t1", "app", "bkt", r2.NewID())
			if err := b.Put(ctx, empty, bytes.NewReader(nil), 0); err != nil {
				t.Fatal(err)
			}
			if err := b.Delete(ctx, name); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Get(ctx, name, 0, -1); !errors.Is(err, r2.ErrNotFound) {
				t.Fatalf("after delete: %v", err)
			}
			if err := b.Delete(ctx, name); err != nil {
				t.Fatalf("second delete: %v", err)
			}
		})
	}
	if fake.Denied.Load() != 0 {
		t.Fatalf("fake S3 refused %d signatures", fake.Denied.Load())
	}
	if ks := fake.Keys(); len(ks) != 1 || !strings.HasPrefix(ks[0], "pfx/t/t1/w/app/b/bkt/") {
		t.Fatalf("fake S3 keys: %v", ks)
	}
}

func TestS3WrongCredentials(t *testing.T) {
	fake := r2test.StartFakeS3(t.TempDir())
	defer fake.Close()
	b := fake.Backend("")
	b.Creds.SecretAccessKey = "wrong"
	name, _ := r2.Name("t1", "app", "bkt", r2.NewID())
	err := b.Put(context.Background(), name, strings.NewReader("x"), 1)
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("put with a wrong secret: %v", err)
	}
}

func TestFlags(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	f := r2.Flags{Backend: "fs"}
	b, err := f.Open("/data", get)
	if err != nil || b.String() != "fs:"+filepath.Join("/data", "r2") {
		t.Fatalf("fs default: %v %v", b, err)
	}
	f = r2.Flags{Backend: "s3", Endpoint: "http://minio:9000", Bucket: "b", Prefix: "tw"}
	if _, err := f.Open("/data", get); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("s3 without credentials: %v", err)
	}
	env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"] = "id", "secret"
	b, err = f.Open("/data", get)
	if err != nil || b.String() != "s3:http://minio:9000/b/tw/" || strings.Contains(b.String(), "secret") {
		t.Fatalf("s3: %v %v", b, err)
	}
	cf := filepath.Join(t.TempDir(), "creds")
	os.WriteFile(cf, []byte("# r2\nexport TW_R2_S3_ACCESS_KEY_ID=k\nTW_R2_S3_SECRET_ACCESS_KEY=\"s\"\n"), 0o600)
	f.CredsFile = cf
	if _, err := f.Open("/data", func(string) string { return "" }); err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	for _, bad := range []r2.Flags{{Backend: "gcs"}, {Backend: "s3", Endpoint: "ftp://x", Bucket: "b"}, {Backend: "s3", Endpoint: "http://u:p@x", Bucket: "b"}, {Backend: "s3", Endpoint: "http://x"}} {
		if _, err := bad.Open("/data", get); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}
