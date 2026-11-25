package hostdrun

import (
	"context"
	"log/slog"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// mgmtAttempt bounds one probe of mgmt while hostd waits for the path.
//
// A node that starts from its netmap cache is Running before control has
// sent its peers, so the first SYN to mgmt's node has no route and is
// dropped. One request then waited out netstack's SYN retransmits (1, 2, 4,
// 8, 16 s): hostd reached mgmt ~31 s after a reboot (#245). A fresh dial per
// attempt is answered as soon as the peer is in the netmap.
const mgmtAttempt = time.Second

// waitMgmt probes mgmt until it answers (any answer means the path is open)
// or ctx ends, and logs how long that took.
func waitMgmt(ctx context.Context, probe func(context.Context) error, log *slog.Logger) error {
	st := time.Now()
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, mgmtAttempt)
		err := probe(actx)
		cancel()
		if _, typed := mgmtapi.AsError(err); err == nil || typed {
			log.Info("mgmt reachable", "ms", time.Since(st).Milliseconds(), "attempts", attempt)
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(250 * time.Millisecond):
		}
	}
}
