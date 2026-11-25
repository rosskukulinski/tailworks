package releasesig

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTarGz unpacks a release tarball into dir (which it creates),
// allowing only files, directories and symlinks that resolve inside dir.
// It runs before the signature check (the signed SHA256SUMS is inside the
// tarball), so it must be safe on hostile input: every write goes through
// an os.Root, which refuses any path that resolves outside dir, including
// through a symlink an earlier entry created (a -> ., a/b -> .., b/evil);
// no entry may replace an existing one; and once everything is out, every
// symlink must resolve inside dir, so nothing that later reads or copies
// the tree (VerifyTree, install.sh) can be led outside it.
func ExtractTarGz(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(zr)
	var links []string
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hd.Name))
		if filepath.IsAbs(hd.Name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("entry %q escapes the archive", hd.Name)
		}
		if name == "." {
			continue
		}
		if parent := filepath.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("entry %q: %w", hd.Name, err)
			}
		}
		switch hd.Typeflag {
		case tar.TypeDir:
			if fi, err := root.Lstat(name); err == nil {
				if !fi.IsDir() {
					return fmt.Errorf("entry %q replaces an earlier entry", hd.Name)
				}
				continue
			}
			if err := root.Mkdir(name, 0o755); err != nil {
				return fmt.Errorf("entry %q: %w", hd.Name, err)
			}
		case tar.TypeReg:
			out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hd.Mode)&0o755|0o600)
			if err != nil {
				return fmt.Errorf("entry %q: %w", hd.Name, err)
			}
			_, err = io.Copy(out, io.LimitReader(tr, 1<<30))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			t := filepath.Join(filepath.Dir(name), hd.Linkname)
			if filepath.IsAbs(hd.Linkname) || t == ".." || strings.HasPrefix(t, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("symlink %q escapes the archive", hd.Name)
			}
			if err := root.Symlink(hd.Linkname, name); err != nil {
				return fmt.Errorf("entry %q: %w", hd.Name, err)
			}
			links = append(links, name)
		default:
			return fmt.Errorf("entry %q: unsupported type %c", hd.Name, hd.Typeflag)
		}
	}
	// The lexical check above can't see symlinks resolved through other
	// symlinks (x -> s/.. where s -> . is in-tree on paper, the parent of
	// dir on disk). Resolve each one inside the root: a link that leads
	// out of it fails here. A dangling in-tree link is fine.
	for _, l := range links {
		if _, err := root.Stat(l); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("symlink %q escapes the archive: %w", l, err)
		}
	}
	return nil
}
