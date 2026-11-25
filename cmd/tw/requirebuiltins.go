package main

import "github.com/rosskukulinski/tailworks/internal/esbundle"

// nodeCompat reports whether the wrangler config's compatibility_flags turn
// on Node.js compatibility, so the built-in bundler turns a require() of a
// Node builtin into an import (issue #356).
func nodeCompat(w map[string]any) bool {
	raw, _ := w["compatibility_flags"].([]any)
	var flags []string
	for _, f := range raw {
		if s, ok := f.(string); ok {
			flags = append(flags, s)
		}
	}
	return esbundle.NodeCompat(flags)
}
