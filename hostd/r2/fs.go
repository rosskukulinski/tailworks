package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FS keeps blobs as files under Root (the homelab and tw dev backend).
// A put writes a temporary file in the blob's directory, syncs it and
// renames it into place, so a blob is either whole or absent.
type FS struct {
	Root string
}

func (f *FS) String() string { return "fs:" + f.Root }

func (f *FS) path(name string) string { return filepath.Join(f.Root, filepath.FromSlash(name)) }

// Put implements Backend.
func (f *FS) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if size < 0 || size > MaxBlob {
		return fmt.Errorf("r2: bad blob size %d", size)
	}
	p := f.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, &ctxReader{ctx, io.LimitReader(r, size)})
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("r2: body ended after %d of %d bytes", n, size)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	ok = true
	return nil
}

// Get implements Backend.
func (f *FS) Get(ctx context.Context, name string, off, n int64) (io.ReadCloser, error) {
	fh, err := os.Open(f.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if _, err := fh.Seek(off, io.SeekStart); err != nil {
			fh.Close()
			return nil, err
		}
	}
	if n < 0 {
		return fh, nil
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(fh, n), fh}, nil
}

// Delete implements Backend.
func (f *FS) Delete(ctx context.Context, name string) error {
	err := os.Remove(f.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ctxReader stops a copy when ctx is done (the caller went away).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
