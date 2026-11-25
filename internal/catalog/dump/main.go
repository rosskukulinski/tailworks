// Command dump prints the embedded release catalog as a JSON array
// (scripts/release.sh writes it to share/tailworks/catalog.json).
package main

import (
	"encoding/json"
	"os"

	"github.com/rosskukulinski/tailworks/internal/catalog"
)

func main() {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(catalog.Embedded()); err != nil {
		os.Exit(1)
	}
}
