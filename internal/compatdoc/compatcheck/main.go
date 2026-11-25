// Command compatcheck runs internal/compatdoc's checks on the repo (make
// lint): every README "Compatibility at a glance" row has a docs/COMPAT.md
// surface row with the same status, and every ✅/⚠️ row names tests that
// exist.
package main

import (
	"fmt"
	"os"

	"github.com/rosskukulinski/tailworks/internal/compatdoc"
)

// usage: compatcheck [repo root, default .]
func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	errs := compatdoc.Check(root)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "compatcheck:", e)
	}
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "compatcheck: %d problem(s); see the format notes at the top of docs/COMPAT.md\n", len(errs))
		os.Exit(1)
	}
}
