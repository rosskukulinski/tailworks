package mgmt

import "context"

// hostAuditHead is the hash of the last record stored on hostID's audit
// chain ("" when the chain is empty). RegisterHost answers it so a host
// that restarted on the same data dir can continue its chain instead of
// starting a new segment at prevHash "" (#377). The host links to it only
// when it equals the last record the host itself chained and flushed
// before a clean shutdown (hostd/auditresume.go), so a chain truncated or
// rewritten here is still reported by VerifyChain, never papered over.
func (m *Mgmt) hostAuditHead(ctx context.Context, hostID string) (string, error) {
	_, head, err := m.store.AuditHead(ctx, "host:"+hostID)
	return head, err
}
