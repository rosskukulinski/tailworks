package procsb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LayoutErrorCode is the typed error for a tailworks path the Landlock
// sandbox can't keep from tenant code (docs/errors.md#sandbox_layout_unsafe).
const LayoutErrorCode = "sandbox_layout_unsafe"

// LayoutPath is one path hostd denies to every sandboxed workload, with
// what it is for the error message ("state dir", "credential file", …).
type LayoutPath struct {
	What string
	Path string
}

// LayoutError says that Path (resolving to Real) lies under Tree, a
// directory every sandboxed workload may read, so denying it has no effect.
type LayoutError struct {
	What string
	Path string
	Real string
	Tree string
}

func (e *LayoutError) Error() string {
	p := e.Path
	if e.Real != "" && e.Real != e.Path {
		p += " (" + e.Real + ")"
	}
	return fmt.Sprintf("%s: the %s %s is under %s, which every Landlock-sandboxed workload may read (Landlock has no deny rules); keep tailworks' state, run dir and credential files outside the sandbox's read-only trees",
		LayoutErrorCode, e.What, p, e.Tree)
}

// CheckLayout returns a *LayoutError for the first of paths that, after
// symlinks, is one of trees or lies below one. Landlock access rights are
// additive along a path: a rule that allows reading /opt allows every file
// below it, and nothing can take a subtree back. hostd's deny list (the
// state and run dirs, credential files, the home dir) therefore protects a
// path only while it is outside every readable tree; this is the check that
// refuses the layouts where it isn't. Empty paths are skipped; a path that
// doesn't exist yet is resolved through its longest existing prefix.
func CheckLayout(paths []LayoutPath, trees []string) error {
	type tree struct{ raw, real string }
	var ts []tree
	for _, t := range trees {
		if t == "" || t == "/" {
			continue
		}
		ts = append(ts, tree{filepath.Clean(t), resolvePath(t)})
	}
	for _, p := range paths {
		if p.Path == "" {
			continue
		}
		real := resolvePath(p.Path)
		for _, t := range ts {
			if under(real, t.real) || under(real, t.raw) {
				return &LayoutError{What: p.What, Path: p.Path, Real: real, Tree: t.raw}
			}
		}
	}
	return nil
}

// under reports whether p is root or below it (both clean and absolute).
func under(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// resolvePath is p absolute with symlinks resolved; a missing path
// resolves its longest existing prefix (as hostd's realPath does for
// Seatbelt), following a dangling symlink (a state dir linked into /opt
// before it is created) by hand.
func resolvePath(p string) string { return resolve(p, 0) }

func resolve(p string, depth int) string {
	p, _ = filepath.Abs(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if depth < 40 {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if tgt, err := os.Readlink(p); err == nil {
				if !filepath.IsAbs(tgt) {
					tgt = filepath.Join(filepath.Dir(p), tgt)
				}
				return resolve(tgt, depth+1)
			}
		}
	}
	dir, base := filepath.Split(p)
	if dir == "" || dir == p || dir == "/" {
		return p
	}
	return filepath.Join(resolve(strings.TrimSuffix(dir, "/"), depth), base)
}
