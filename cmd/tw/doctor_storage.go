package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw doctor's storage checks (#120): free disk where tailworks keeps its
// data, and each tenant's stores and queues against their limits.

// Free-disk thresholds for the data dir (mgmt.db, custody, DO storage,
// logs). Below doctorDiskFail free, mgmt and the hosts start failing
// writes; below doctorDiskWarn (or doctorDiskWarnPct of the volume) it is
// time to act.
const (
	doctorDiskFail    = 1 << 30
	doctorDiskWarn    = 5 << 30
	doctorDiskWarnPct = 10
	// doctorStoreWarnPct: a store, queue or tenant quota this full warns.
	doctorStoreWarnPct = 80
)

func doctorStorage(run func(string, func() (string, string, string)), s *state) {
	run("data_disk", func() (string, string, string) { return doctorDataDisk(s) })
	run("store_sizes", func() (string, string, string) { return doctorStoreSizes(s) })
}

// diskFree is the space available to this user on dir's volume, and the
// volume's size.
func diskFree(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}

func doctorDataDisk(s *state) (string, string, string) {
	if s == nil {
		return "skip", "no state", ""
	}
	if s.remote() {
		return "skip", "this machine is linked to " + s.Link.Endpoint + ": its data is on that host (run tw doctor there)", ""
	}
	dir := s.Dir
	for !dirExists(dir) && filepath.Dir(dir) != dir {
		dir = filepath.Dir(dir) // not created yet: the volume it will be on
	}
	free, total, err := diskFree(dir)
	if err != nil {
		return "warn", "can't read free space on " + dir + ": " + err.Error(), ""
	}
	d := fmt.Sprintf("%s free of %s on the volume holding %s", humanBytes(int64(free)), humanBytes(int64(total)), s.Dir)
	if n := fileSizes(filepath.Join(s.Dir, "mgmt", "mgmt.db")); n > 0 {
		d += "; mgmt.db " + humanBytes(n)
	}
	fix := "free space on that volume, or move " + s.Dir + " to a bigger one (tw down, move it, tw up --state-dir); `tw tenant ls` shows what each tenant stores"
	switch {
	case free < doctorDiskFail:
		return "fail", d + fmt.Sprintf(": under %s, mgmt and the host will start failing writes", humanBytes(doctorDiskFail)), fix
	case free < doctorDiskWarn || (total > 0 && free*100 < total*doctorDiskWarnPct):
		return "warn", d + fmt.Sprintf(": under %s or %d%% free", humanBytes(doctorDiskWarn), doctorDiskWarnPct), fix
	}
	return "ok", d, ""
}

// fileSizes is the size of a SQLite database with its -wal and -shm.
func fileSizes(path string) int64 {
	var n int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}

func doctorStoreSizes(s *state) (string, string, string) {
	switch {
	case s == nil || (!s.configured() && !s.remote()):
		return "skip", "tw up not run here", ""
	case !s.remote() && !s.supervisorAlive():
		return "skip", "tw is not running here", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var warns []string
	var tenants, stores, queues int64
	var biggest mgmt.UsageItem
	for _, t := range s.tenants() {
		u := s.tenantUsage(ctx, t.TenantID)
		if u == nil {
			continue
		}
		tenants++
		stores += u.Stores.Count
		queues += u.Queues.Count
		warns = append(warns, storageWarnings(t.TenantID, u)...)
		for _, it := range u.Largest {
			if it.Bytes > biggest.Bytes {
				biggest = it
			}
		}
	}
	if tenants == 0 {
		return "skip", "mgmt reported no tenant's usage (an older tw-mgmt?)", ""
	}
	if len(warns) > 0 {
		return "warn", strings.Join(warns, "; "), "delete data that is no longer needed, consume or purge queues, or raise a tenant quota (tw tenant set <tenant> --max-store-bytes …); one store or queue can't grow past its 1 GiB"
	}
	d := fmt.Sprintf("%d tenants, %d stores, %d queues, all under %d%% of their limits", tenants, stores, queues, doctorStoreWarnPct)
	if biggest.Bytes > 0 {
		d += fmt.Sprintf("; largest %s (%s)", itemName(biggest), ofQuota(biggest.Bytes, biggest.Limit))
	}
	return "ok", d, ""
}

// storageWarnings lists what is at least doctorStoreWarnPct full: a
// tenant's quotas and its largest stores and queues.
func storageWarnings(tenantID string, u *mgmt.TenantUsage) []string {
	full := func(used, limit int64) bool { return limit > 0 && used*100 >= limit*doctorStoreWarnPct }
	var w []string
	if full(u.Stores.Bytes, u.Limits.MaxStoreBytes) {
		w = append(w, fmt.Sprintf("tenant %s stores at %s of maxStoreBytes", tenantID, ofQuota(u.Stores.Bytes, u.Limits.MaxStoreBytes)))
	}
	if full(u.Queues.Bytes, u.Limits.MaxQueueBytes) {
		w = append(w, fmt.Sprintf("tenant %s queues at %s of maxQueueBytes", tenantID, ofQuota(u.Queues.Bytes, u.Limits.MaxQueueBytes)))
	}
	if full(u.Stores.Count, int64(u.Limits.MaxStores)) {
		w = append(w, fmt.Sprintf("tenant %s has %d of its maxStores %d", tenantID, u.Stores.Count, u.Limits.MaxStores))
	}
	for _, it := range u.Largest {
		if full(it.Bytes, it.Limit) {
			w = append(w, fmt.Sprintf("tenant %s %s at %s", tenantID, itemName(it), ofQuota(it.Bytes, it.Limit)))
		}
	}
	return w
}

func itemName(it mgmt.UsageItem) string {
	if it.Kind == "queue" {
		return "queue " + it.Name
	}
	return it.Kind + " " + it.Workload + "/" + it.Name
}
