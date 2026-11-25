package mgmt

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// TestAuditVerifyConcurrentAppend is #93: VerifyChain on an intact chain
// must never report "chain head mismatch" while another writer appends.
// The records and the head used to be read in two separate queries, so an
// append that committed between them left the head one record ahead.
func TestAuditVerifyConcurrentAppend(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		const n = 300
		var appended atomic.Int64
		done := make(chan error, 1)
		go func() {
			for i := range n {
				if _, err := s.AppendAudit(ctx, AuditRecord{Chain: "mgmt", TS: fmt.Sprintf("%08d", i), Kind: "spawn", ObjectID: fmt.Sprintf("o%d", i)}); err != nil {
					done <- err
					return
				}
				appended.Add(1)
			}
			done <- nil
		}()
		verifies := 0
		for {
			st := VerifyChain(ctx, s, "mgmt", nil)
			verifies++
			if !st.OK {
				<-done
				t.Fatalf("verify %d (after %d appends): %+v", verifies, appended.Load(), st)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				if st := VerifyChain(ctx, s, "mgmt", nil); !st.OK || st.Records != n {
					t.Fatalf("final: %+v", st)
				}
				t.Logf("%d verifies during %d appends", verifies, n)
				return
			default:
			}
		}
	})
}
