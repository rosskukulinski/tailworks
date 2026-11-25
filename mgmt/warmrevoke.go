package mgmt

import (
	"context"
	"sync"
)

// warmRevokeParallel bounds the concurrent key revocations of a drain.
// One revocation is one API round trip; done one by one, a pool of a few
// dozen keys held `tw down` for seconds (#245).
const warmRevokeParallel = 8

// tenantKey is a warm key and its tenant, to revoke.
type tenantKey struct {
	t  Tenant
	wk WarmKey
}

// revokeWarmAll revokes ks, up to warmRevokeParallel at a time, and
// returns when all are done (each one as revokeWarm: a failure keeps the
// row for the sweep).
func (m *Mgmt) revokeWarmAll(ctx context.Context, ks []tenantKey) {
	sem := make(chan struct{}, warmRevokeParallel)
	var wg sync.WaitGroup
	for _, k := range ks {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			m.revokeWarm(ctx, k.t, k.wk)
		}()
	}
	wg.Wait()
}
