package mgmt

import (
	"context"
	"testing"
)

// A history read that races a rollback must not erase its rollbackOf
// (#563, found by the e2e/dataplane deploys loop). The interleaving:
// the rollback's record commits (no hash, no rollbackOf yet); a reader
// (tw deployments, or another rollback's rollbackTarget) loads it and, in
// fillRecord, backfills the hash with the rollbackOf it read (""); the
// rollback's afterDeploy records hash and rollbackOf; the reader's write
// lands last. SQLite always; Postgres with TW_TEST_POSTGRES_DSN.
func TestStoreSetDeploymentMetaKeepsRollbackOf(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s *SQLStore) {
		ctx := context.Background()
		b := []byte("bundle-rb")
		if err := s.CreateDeployment(ctx, Deployment{ID: "dp_rb", TenantID: "tn", Workload: "web", HostID: "h1", Epoch: 7, Bundle: b, CreatedAt: 1000}); err != nil {
			t.Fatal(err)
		}
		stale, err := s.DeploymentRecordByID(ctx, "dp_rb") // the reader's copy
		if err != nil || stale.RollbackOf != "" || stale.BundleSHA256 != "" {
			t.Fatalf("fresh record: %+v %v", stale, err)
		}
		if err := s.SetDeploymentMeta(ctx, "dp_rb", bundleSHA256(b), "dp_target"); err != nil { // afterDeploy
			t.Fatal(err)
		}
		if err := s.SetDeploymentMeta(ctx, "dp_rb", bundleSHA256(b), stale.RollbackOf); err != nil { // fillRecord, late
			t.Fatal(err)
		}
		got, err := s.DeploymentRecordByID(ctx, "dp_rb")
		if err != nil || got.RollbackOf != "dp_target" || got.BundleSHA256 != bundleSHA256(b) {
			t.Fatalf("after a racing backfill: rollbackOf %q hash %q (%v); want dp_target and the bundle's hash", got.RollbackOf, got.BundleSHA256, err)
		}
		// An empty hash doesn't erase a recorded one either.
		if err := s.SetDeploymentMeta(ctx, "dp_rb", "", ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.DeploymentRecordByID(ctx, "dp_rb"); got.BundleSHA256 != bundleSHA256(b) || got.RollbackOf != "dp_target" {
			t.Fatalf("after an empty write: %+v", got)
		}
	})
}
