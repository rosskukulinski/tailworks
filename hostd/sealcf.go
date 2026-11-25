package hostd

import (
	"encoding/json"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// sealCaller adds cf.tailscale.seal to netd's cf blob (RT3-08 / H08, #148):
// the caller netd got from WhoIs, sealed under the instance root key, so
// the platform and runtime can tell it from a caller tenant code attached to
// an in-process forward. A blob without a caller (public requests) gets no
// seal; a client-sent seal never survives (netd strips x-tw-cf, and this
// overwrites the field).
func sealCaller(blob map[string]any, root string, now time.Time) {
	ts, _ := blob["tailscale"].(map[string]any)
	if ts == nil {
		return
	}
	delete(ts, "seal")
	c, ok := ts["caller"]
	if !ok || c == nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	if s := gen.SealCaller(root, b, now); s != nil {
		ts["seal"] = s
	}
}
