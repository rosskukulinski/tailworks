package main

// Degraded platform stores (#147) in the CLI's listings: a store whose
// object stopped its replay at a journal entry it can't apply
// (store_degraded; mgmt's StoreInfo.Degraded).

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

func degradedSeq(d *mgmt.StoreDegraded) int64 {
	if d == nil {
		return 0
	}
	return d.Seq
}

// degradedLine describes d in one line ("" for nil).
func degradedLine(d *mgmt.StoreDegraded) string {
	if d == nil {
		return ""
	}
	where := ""
	if d.Host != "" {
		where = " on host " + d.Host
	}
	return fmt.Sprintf("degraded: journal entry %d failed to apply%s %s (%s); reads serve what applied before it, writes are refused",
		d.Seq, where, time.UnixMilli(d.At).Local().Format("2006-01-02 15:04:05"), d.Error)
}

// printStoreDegraded prints the degraded line under a listing's row, and
// fix, when d is set.
func printStoreDegraded(d *mgmt.StoreDegraded, fix string) {
	if d == nil {
		return
	}
	fmt.Printf("  ✗ %s\n", tsafe(degradedLine(d))) // may quote a host error
	if fix != "" {
		fmt.Printf("    fix: %s\n", fix)
	}
}

// degradedStores lists the stores mgmt records as degraded (tenant "": all
// it lets the caller see). An error (an older mgmt) is no stores.
func degradedStores(ctx context.Context, c *mgmtapi.Client, tenant string) []mgmt.StoreInfo {
	path := "/v1/stores/degraded"
	if tenant != "" {
		path += "?tenant=" + url.QueryEscape(tenant)
	}
	var out struct {
		Items []mgmt.StoreInfo `json:"items"`
	}
	if c.Do(ctx, "GET", path, nil, &out) != nil {
		return nil
	}
	return out.Items
}

// storeFix is the way out for a degraded store of a kind.
func storeFix(st mgmt.StoreInfo) string {
	if st.Kind == "d1" {
		wl := " --workload " + st.Workload
		return fmt.Sprintf("tw d1 export %s%s --output backup.sql, then tw d1 repair %s --skip %d%s", st.Name, wl, st.Name, degradedSeq(st.Degraded), wl)
	}
	return "upgrade tw-hostd if a newer tailworks wrote the entry; otherwise see docs/errors.md#store_degraded"
}

// printDegradedStores is tw status' section: one ✗ line per degraded
// store.
func printDegradedStores(sts []mgmt.StoreInfo) {
	for _, st := range sts {
		fmt.Printf("✗ store       %s %s of workload %s (tenant %s) · %s\n", st.Kind, st.Name, st.Workload, st.TenantID, strings.TrimPrefix(degradedLine(st.Degraded), "degraded: "))
		fmt.Printf("    fix: %s\n", storeFix(st))
	}
}
