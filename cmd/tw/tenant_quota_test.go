package main

import (
	"context"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCLITenantQuotas (#120): tw tenant set sets and clears the storage
// quotas, tw tenant ls shows each tenant's usage against them, and an
// append past the quota is refused with storage_quota_exceeded.
func TestCLITenantQuotas(t *testing.T) {
	e := newPubEnv(t)
	ctx := context.Background()
	out, err := e.call(t, cmdTenant, "set", e.tenantID, "--max-store-bytes", "1KiB", "--max-queue-bytes", "2MB", "--max-stores", "3")
	if err != nil || !strings.Contains(out, "storage quotas: stores 1.0 KiB, queues 1.9 MiB, store count 3") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if _, err := e.m.AppendStore(ctx, e.tenantID, "support", "kv", "cache", "", nil, 0, "", []mgmt.StoreEntryIn{{CKey: "k", Data: make([]byte, 512)}}); err != nil {
		t.Fatal(err)
	}
	out, err = e.call(t, cmdTenant, "ls")
	if err != nil || !strings.Contains(out, "stores 512 B / 1.0 KiB (50%) (1/3) · queues 0 (0 B / 1.9 MiB") {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	if out, err := e.call(t, cmdTenant, "ls", "--json"); err != nil || !strings.Contains(out, `"maxStoreBytes": 1024`) {
		t.Fatalf("ls --json: %v\n%s", err, out)
	}
	_, err = e.m.AppendStore(ctx, e.tenantID, "support", "kv", "other", "", nil, 0, "", []mgmt.StoreEntryIn{{Data: make([]byte, 600)}})
	if me, ok := err.(*mgmt.Error); !ok || me.Code != "storage_quota_exceeded" {
		t.Fatalf("over the quota: %v", err)
	}
	if _, err := e.call(t, cmdTenant, "set", e.tenantID, "--max-store-bytes", "lots"); err == nil {
		t.Fatal("a bad size was accepted")
	}
	out, err = e.call(t, cmdTenant, "set", e.tenantID, "--max-store-bytes", "-", "--max-stores", "0")
	if err != nil || !strings.Contains(out, "storage quotas: stores unlimited, queues 1.9 MiB, store count unlimited") {
		t.Fatalf("clear: %v\n%s", err, out)
	}
}

func TestParseByteSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"0": 0, "1024": 1024, "1KiB": 1024, "2 MB": 2e6, "1.5GiB": 3 << 29, "20gb": 20e9, "1t": 1 << 40} {
		if got, err := parseByteSize(in); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "1XB", "1..2G"} {
		if _, err := parseByteSize(in); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}

// TestDoctorStorage (#120): tw doctor reports free disk on the data dir
// and warns when a store, queue or tenant quota is 80% full.
func TestDoctorStorage(t *testing.T) {
	e := newPubEnv(t)
	ctx := context.Background()
	s, err := openState(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, d, _ := doctorDataDisk(s); st == "skip" || !strings.Contains(d, "free of") {
		t.Fatalf("data_disk: %s %s", st, d)
	}
	if _, err := e.m.AppendStore(ctx, e.tenantID, "support", "kv", "cache", "", nil, 0, "", []mgmt.StoreEntryIn{{Data: make([]byte, 700)}}); err != nil {
		t.Fatal(err)
	}
	if st, d, _ := doctorStoreSizes(s); st != "ok" || !strings.Contains(d, "1 tenants, 1 stores, 0 queues") || !strings.Contains(d, "largest kv support/cache (700 B / 1.0 GiB") {
		t.Fatalf("store_sizes: %s %s", st, d)
	}
	if _, err := e.call(t, cmdTenant, "set", e.tenantID, "--max-store-bytes", "800"); err != nil {
		t.Fatal(err)
	}
	if st, d, fix := doctorStoreSizes(s); st != "warn" || !strings.Contains(d, "tenant "+e.tenantID+" stores at 700 B / 800 B (88%) of maxStoreBytes") || fix == "" {
		t.Fatalf("store_sizes near the quota: %s %s", st, d)
	}
	// A store near its own 1 GiB warns whatever the quota.
	u := &mgmt.TenantUsage{Largest: []mgmt.UsageItem{{Kind: "d1", Workload: "w", Name: "db", Bytes: 900 << 20, Limit: 1 << 30}, {Kind: "queue", Name: "q", Bytes: 1, Limit: 1 << 30}}}
	if w := storageWarnings("tn_x", u); len(w) != 1 || !strings.Contains(w[0], "tenant tn_x d1 w/db at 900.0 MiB / 1.0 GiB (88%)") {
		t.Fatalf("warnings: %q", w)
	}
}
