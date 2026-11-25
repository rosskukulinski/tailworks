package main

import (
	"fmt"
	"io"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// printDevEgressVia: tw dev has no exit node (its control is local,
// ADR-014), so a workload with tailnet.egress.via "exit-node" runs on
// this machine's network here, and says so once (ADR-054).
func printDevEgressVia(twRaw []byte, w io.Writer) {
	var top struct {
		Tailnet struct {
			Egress struct {
				Via string `json:"via"`
			} `json:"egress"`
		} `json:"tailnet"`
	}
	if len(twRaw) == 0 || bundle.UnmarshalJSONC(twRaw, &top) != nil {
		return
	}
	if top.Tailnet.Egress.Via == bundle.ViaExitNode {
		fmt.Fprintf(w, "note: tailnet.egress.via is %q; tw dev has no exit node, so internet egress leaves from this machine's network here\n", top.Tailnet.Egress.Via)
	}
}
