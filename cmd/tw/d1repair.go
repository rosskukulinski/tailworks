package main

// `tw d1 repair <db> --skip <seq>` (#147): a D1 database whose store
// object stopped its replay at a journal entry it can't apply
// (store_degraded) serves reads from what it applied and refuses writes.
// When the operator decides that entry is poison, repair deletes it from
// mgmt's journal (POST …/stores/d1/{name}/repair), so every host's replay
// steps over it; the change it held is gone for good. mgmt refuses unless
// it records the database as degraded at exactly that entry, and audits
// the repair (store_repair).

import (
	"context"
	"fmt"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// confirmD1Repair checks --skip and asks before a repair (confirm.go).
func confirmD1Repair(f *flags, yes bool, name string, skip int64) error {
	if skip < 1 {
		return fail("bad_request", "tw d1 repair needs --skip <seq>: the journal entry the database is degraded at").
			withFix("tw d1 ls   (shows a degraded database and the entry it stopped at)")
	}
	return confirmDestroy(f, yes, fmt.Sprintf("repairing database %s deletes journal entry %d from it: the write it held is lost on every host", name, skip),
		name, fmt.Sprintf("tw d1 repair %s --skip %d", name, skip),
		"tw d1 export "+name+" --output backup.sql   (first, to keep a copy of what applied)")
}

// d1Repair skips journal entry skip of the database at storePath.
func d1Repair(ctx context.Context, c *mgmtapi.Client, f *flags, res *d1Result, storePath string, skip int64, hdr map[string]string) error {
	var st mgmt.StoreInfo
	if err := c.DoHeaders(ctx, "POST", storePath+"/repair", map[string]any{"skip": skip}, &st, hdr); err != nil {
		if e, ok := mgmtapi.AsError(err); ok && e.Code == "store_not_degraded" {
			return fail(e.Code, e.Message).withFix("tw d1 ls   (shows the entry a degraded database stopped at)")
		}
		return err
	}
	res.Repaired, res.Skipped = &st, skip
	emit(f, *res, func() {
		fmt.Printf("✓ skipped journal entry %d of database %s (workload %s)\n", skip, res.Database, res.Workload)
		fmt.Printf("  the workload applies the entries after it on its next write, or its next read within a second\n")
	})
	return nil
}
