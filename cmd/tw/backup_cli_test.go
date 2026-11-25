package main_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// backupRestoreRunning is tw backup on a running install (issues #120, #295):
// the backup is taken while tailworks serves, tw restore is refused while it
// runs, and after tw down the restore puts it back and tw up serves the
// same tenant and deployments again.
func backupRestoreRunning(t *testing.T, e *cliflow.Env, work string) {
	t.Helper()
	type lsOut struct {
		TenantID    string
		Deployments []struct{ ID, Workload string }
	}
	var before lsOut
	e.MustJSON(&before, "ls")
	bk := filepath.Join(work, "backup")
	var br struct {
		Backup   string
		Manifest struct {
			Store, Custody string
			Hostd          *struct{ Workloads []string } // issue #295
		}
	}
	e.MustJSON(&br, "backup", "--out", bk)
	if br.Backup != bk || br.Manifest.Store != "sqlite" || br.Manifest.Custody != "file" || br.Manifest.Hostd == nil || len(br.Manifest.Hostd.Workloads) == 0 {
		t.Fatalf("tw backup: %+v", br)
	}
	if _, se, _, err := e.Tw("restore", bk, "--yes"); err == nil || !strings.Contains(se, "already_running") {
		t.Fatalf("tw restore while tailworks runs: %v %s", err, se)
	}
	e.Down()
	var rr struct {
		RestoreGeneration int64
		SafetyBackup      string
	}
	e.MustJSON(&rr, "restore", bk, "--yes")
	if rr.RestoreGeneration != 1 || rr.SafetyBackup == "" {
		t.Fatalf("tw restore: %+v", rr)
	}
	if so, se, _, err := e.Tw(e.UpCommand()...); err != nil {
		t.Fatalf("tw up after the restore: %v\n%s\n%s", err, so, se)
	}
	var after lsOut
	e.MustJSON(&after, "ls")
	if after.TenantID != before.TenantID || fmt.Sprint(after.Deployments) != fmt.Sprint(before.Deployments) {
		t.Fatalf("after the restore: %+v, before %+v", after, before)
	}
}
