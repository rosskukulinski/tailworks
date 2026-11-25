package main

import (
	"fmt"

	"github.com/rosskukulinski/tailworks/schema"
)

// checkTwSchema refuses a tailworks.jsonc with keys or values tailworks doesn't know,
// so a typo ("objcts") fails here instead of deploying silently without it.
func checkTwSchema(raw []byte) error {
	probs, err := schema.CheckTw(raw)
	if err != nil {
		return fail("bad_request", "tailworks.jsonc: "+err.Error())
	}
	if len(probs) == 0 {
		return nil
	}
	lines := make([]string, len(probs))
	for i, p := range probs {
		lines[i] = p.String()
	}
	msg := "tailworks.jsonc has 1 problem"
	if len(probs) > 1 {
		msg = fmt.Sprintf("tailworks.jsonc has %d problems", len(probs))
	}
	return fail("bad_request", msg, lines...).
		withFix("every key is listed in docs/tailworks-jsonc.md; add \"$schema\" for editor completion (see that page)")
}
