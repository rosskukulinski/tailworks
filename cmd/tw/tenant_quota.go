package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// Per-tenant storage quotas (#120): `tw tenant set --max-store-bytes
// --max-queue-bytes --max-stores`, and the usage `tw tenant ls` and
// `tw doctor` show (GET /v1/tenants/{id}/usage).

type tenantQuotaFlags struct {
	storeBytes, queueBytes, stores *string
}

func addTenantQuotaFlags(f *flags) tenantQuotaFlags {
	return tenantQuotaFlags{
		storeBytes: f.String("max-store-bytes", "", "storage quota: bytes of all the tenant's KV, D1 and R2 journals together, e.g. 20GiB (\"-\" or 0 = unlimited, the default)"),
		queueBytes: f.String("max-queue-bytes", "", "storage quota: bytes of all the tenant's queued messages together, e.g. 2GiB (\"-\" or 0 = unlimited, the default)"),
		stores:     f.String("max-stores", "", "storage quota: how many KV namespaces, D1 databases and R2 buckets the tenant may have (\"-\" or 0 = unlimited, the default)"),
	}
}

// apply adds the given quota flags to a tenant merge patch.
func (q tenantQuotaFlags) apply(patch map[string]any) error {
	for _, fl := range []struct {
		flag, field, v string
		size           bool
	}{
		{"--max-store-bytes", mgmt.QuotaMaxStoreBytes, *q.storeBytes, true},
		{"--max-queue-bytes", mgmt.QuotaMaxQueueBytes, *q.queueBytes, true},
		{"--max-stores", mgmt.QuotaMaxStores, *q.stores, false},
	} {
		switch {
		case fl.v == "":
			continue
		case fl.v == "-":
			patch[fl.field] = nil
			continue
		}
		var n int64
		var err error
		if fl.size {
			n, err = parseByteSize(fl.v)
		} else {
			n, err = strconv.ParseInt(fl.v, 10, 32)
		}
		if err != nil || n < 0 {
			return fail("bad_request", fl.flag+" "+fl.v+": want a number ≥ 0"+map[bool]string{true: " with an optional unit (B, KB, KiB, MB, MiB, GB, GiB, TB, TiB)"}[fl.size], "usage: tw "+usageOf("tenant"))
		}
		patch[fl.field] = n
	}
	return nil
}

// parseByteSize reads "1048576", "512MiB", "20GB", "1.5 GiB".
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	num, unit := s, ""
	if i >= 0 {
		num, unit = s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	}
	mult := map[string]float64{"": 1, "b": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
		"k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40}[unit]
	if mult == 0 {
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, err
	}
	if f*mult > 1<<62 {
		return 0, fmt.Errorf("%s is too big", s)
	}
	return int64(f * mult), nil
}

// tenantUsage reads a tenant's storage usage (nil if mgmt can't say:
// an older tw-mgmt has no usage endpoint).
func (s *state) tenantUsage(ctx context.Context, tenantID string) *mgmt.TenantUsage {
	var u mgmt.TenantUsage
	if err := s.mgmt().Do(ctx, "GET", "/v1/tenants/"+tenantID+"/usage", nil, &u); err != nil {
		return nil
	}
	return &u
}

// ofQuota prints "used / limit" ("used / ∞" for no quota).
func ofQuota(used, limit int64) string {
	if limit <= 0 {
		return humanBytes(used) + " / ∞"
	}
	return fmt.Sprintf("%s / %s (%.0f%%)", humanBytes(used), humanBytes(limit), 100*float64(used)/float64(limit))
}

// usageLine is `tw tenant ls`'s STORAGE column.
func usageLine(u *mgmt.TenantUsage) string {
	if u == nil {
		return "?"
	}
	stores := fmt.Sprintf("%d", u.Stores.Count)
	if u.Limits.MaxStores > 0 {
		stores += fmt.Sprintf("/%d", u.Limits.MaxStores)
	}
	return fmt.Sprintf("stores %s (%s) · queues %d (%s)", ofQuota(u.Stores.Bytes, u.Limits.MaxStoreBytes), stores,
		u.Queues.Count, ofQuota(u.Queues.Bytes, u.Limits.MaxQueueBytes))
}

// quotaSummary is `tw tenant set`'s line about the storage quotas.
func quotaSummary(t mgmt.Tenant) string {
	q := func(n int64) string {
		if n <= 0 {
			return "unlimited"
		}
		return humanBytes(n)
	}
	stores := "unlimited"
	if t.MaxStores > 0 {
		stores = strconv.Itoa(t.MaxStores)
	}
	return fmt.Sprintf("storage quotas: stores %s, queues %s, store count %s", q(t.MaxStoreBytes), q(t.MaxQueueBytes), stores)
}
