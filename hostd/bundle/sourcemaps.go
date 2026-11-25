package bundle

import "strings"

// addSourceMap keeps a bundler source map (<module>.js.map) next to the
// modules. Only maps of JavaScript modules are kept; workerd never loads
// them.
func (a *App) addSourceMap(p string, b []byte) {
	if strings.HasPrefix(p, "node_modules/") || !strings.HasSuffix(p, ".js.map") && !strings.HasSuffix(p, ".mjs.map") {
		return
	}
	if a.SourceMaps == nil {
		a.SourceMaps = map[string][]byte{}
	}
	a.SourceMaps[p] = b
}
