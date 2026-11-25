// Command protocols prints the protocol versions this tree speaks as JSON,
// for scripts/release.sh's release.json "protocols".
package main

import (
	"encoding/json"
	"os"

	"github.com/rosskukulinski/tailworks/hostd/vm/helperapi"
	"github.com/rosskukulinski/tailworks/internal/version"
)

func main() {
	json.NewEncoder(os.Stdout).Encode(version.Protocols(helperapi.Version))
}
