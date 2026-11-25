package esbundle

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/rosskukulinski/tailworks/internal/emailstub"
)

// emailImports resolves `cloudflare:email` (internal/emailstub, refuse.go):
// a dependency's import (under node_modules) gets the stub, the app's own
// import is refused and recorded, so BuildApp can return an
// emailstub.ImportError naming every importer.
type emailImports struct {
	dir     string
	mu      sync.Mutex
	refused []string
}

func (e *emailImports) resolve(a api.OnResolveArgs) (api.OnResolveResult, error) {
	if emailstub.Dependency(a.Importer) {
		return api.OnResolveResult{Path: "email", Namespace: "tw-stub"}, nil
	}
	imp := e.rel(a.Importer)
	e.mu.Lock()
	if !slices.Contains(e.refused, imp) {
		e.refused = append(e.refused, imp)
	}
	e.mu.Unlock()
	return api.OnResolveResult{Errors: []api.Message{{Text: emailstub.Refusal(imp)}}}, nil
}

// rel is importer relative to the app directory (or to its real path, as
// esbuild may report a symlinked directory's real path), else as it is.
func (e *emailImports) rel(importer string) string {
	dirs := []string{e.dir}
	if real, err := filepath.EvalSymlinks(e.dir); err == nil && real != e.dir {
		dirs = append(dirs, real)
	}
	for _, d := range dirs {
		if r, err := filepath.Rel(d, importer); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return importer
}

// err is the refusal, or nil when no app module imported cloudflare:email.
func (e *emailImports) err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.refused) == 0 {
		return nil
	}
	slices.Sort(e.refused)
	return &emailstub.ImportError{Importers: e.refused}
}
