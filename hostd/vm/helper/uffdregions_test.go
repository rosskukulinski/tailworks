package helper

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const (
	testMemLen = 8 << 20
	testBase   = 0x7f0000000000
	mib        = 1 << 20
)

func regionsJSON(t testing.TB, rs ...uffdRegion) []byte {
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRT3_H11_UffdRegionValidation: the region table the jailed VMM sends
// in the uffd handshake is validated field by field (security/LEDGER.md
// H11). Before the fix, only the page size and a wrapping Offset+Size were
// checked, and a table such as "wrapped offset" below made copyPage index
// the memory file at 2^64-4096 and panic the root helper.
func TestRT3_H11_UffdRegionValidation(t *testing.T) {
	r := func(base, size, off, ps uint64) uffdRegion {
		return uffdRegion{Base: base, Size: size, Offset: off, PageSize: ps}
	}
	many := make([]uffdRegion, uffdMaxRegions+1)
	for i := range many {
		many[i] = r(testBase+uint64(i)*4096, 4096, 0, 4096)
	}
	for _, tc := range []struct {
		name string
		raw  string       // used when set
		rs   []uffdRegion // otherwise
		want string       // "" = accepted; else a substring of the error
	}{
		// Accepted: what Firecracker sends.
		{name: "one 4k region", rs: []uffdRegion{r(testBase, testMemLen, 0, 4096)}},
		{name: "two regions (x86 gap)", rs: []uffdRegion{r(testBase, 4*mib, 0, 4096), r(testBase+1<<32, 4*mib, 4*mib, 4096)}},
		{name: "hugepages", rs: []uffdRegion{r(testBase, 8*mib, 0, 2*mib)}},
		{name: "adjacent regions", rs: []uffdRegion{r(testBase, 4096, 0, 4096), r(testBase+4096, 4096, 4096, 4096)}},
		{name: "unsorted regions", rs: []uffdRegion{r(testBase+4096, 4096, 0, 4096), r(testBase, 4096, 4096, 4096)}},
		{name: "same file range twice", rs: []uffdRegion{r(testBase, 4096, 0, 4096), r(testBase+mib, 4096, 0, 4096)}},
		{name: "region ends at end of file", rs: []uffdRegion{r(testBase, 4096, testMemLen-4096, 4096)}},
		{name: "unknown fields ignored", raw: `[{"base_host_virt_addr":139637976727552,"size":4096,"offset":0,"page_size":4096,"extra":1}]`},

		// Rejected: decoding.
		{name: "not json", raw: `{`, want: "bad mappings"},
		{name: "object not array", raw: `{"size":4096}`, want: "bad mappings"},
		{name: "negative size", raw: `[{"base_host_virt_addr":0,"size":-4096,"offset":0,"page_size":4096}]`, want: "bad mappings"},
		{name: "size past uint64", raw: `[{"base_host_virt_addr":0,"size":18446744073709551616,"offset":0,"page_size":4096}]`, want: "bad mappings"},
		{name: "null", raw: `null`, want: "empty region table"},
		{name: "empty", raw: `[]`, want: "empty region table"},
		{name: "too many regions", rs: many, want: "regions (at most"},

		// Rejected: page size.
		{name: "page size 0", rs: []uffdRegion{r(testBase, 4096, 0, 0)}, want: "page size"},
		{name: "page size not power of two", rs: []uffdRegion{r(testBase, 3*4096, 0, 3*4096)}, want: "page size"},
		{name: "page size below 4k", rs: []uffdRegion{r(testBase, 4096, 0, 2048)}, want: "page size"},
		{name: "page size above 1g", rs: []uffdRegion{r(0, 2<<30, 0, 2<<30)}, want: "page size"},

		// Rejected: base, size, offset.
		{name: "unaligned base", rs: []uffdRegion{r(testBase+1, 4096, 0, 4096)}, want: "base is not page aligned"},
		{name: "base not hugepage aligned", rs: []uffdRegion{r(testBase+4096, 2*mib, 0, 2*mib)}, want: "base is not page aligned"},
		{name: "zero size", rs: []uffdRegion{r(testBase, 0, 0, 4096)}, want: "size is not a whole number"},
		{name: "partial page", rs: []uffdRegion{r(testBase, 4096+1, 0, 4096)}, want: "size is not a whole number"},
		{name: "base + size wraps", rs: []uffdRegion{r(math.MaxUint64-4095, 2*4096, 0, 4096)}, want: "base + size wraps"},
		{name: "wrapped offset", rs: []uffdRegion{r(testBase, 2*4096, math.MaxUint64-4095, 4096)}, want: "past the end"},
		{name: "offset wraps to zero", rs: []uffdRegion{r(testBase, 1<<63, 1<<63, 4096)}, want: "past the end"},
		{name: "unaligned offset", rs: []uffdRegion{r(testBase, 4096, 1, 4096)}, want: "offset is not 4 KiB aligned"},
		{name: "offset at end of file", rs: []uffdRegion{r(testBase, 4096, testMemLen, 4096)}, want: "past the end"},
		{name: "offset past end of file", rs: []uffdRegion{r(testBase, 4096, testMemLen+4096, 4096)}, want: "past the end"},
		{name: "size past end of file", rs: []uffdRegion{r(testBase, testMemLen+4096, 0, 4096)}, want: "past the end"},
		{name: "second region bad", rs: []uffdRegion{r(testBase, 4096, 0, 4096), r(testBase+mib, 4096, 0, 0)}, want: "bad region 1"},

		// Rejected: overlap in guest memory.
		{name: "same base twice", rs: []uffdRegion{r(testBase, 4096, 0, 4096), r(testBase, 4096, 4096, 4096)}, want: "overlap"},
		{name: "partial overlap", rs: []uffdRegion{r(testBase, 2*4096, 0, 4096), r(testBase+4096, 2*4096, 2*4096, 4096)}, want: "overlap"},
		{name: "contained, unsorted", rs: []uffdRegion{r(testBase+4096, 4096, 0, 4096), r(testBase, 4*4096, 4096, 4096)}, want: "overlap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.raw)
			if tc.raw == "" {
				b = regionsJSON(t, tc.rs...)
			}
			got, err := parseRegions(b, testMemLen)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("rejected a valid table: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("accepted %s; want an error containing %q", b, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error %q; want it to contain %q", err, tc.want)
			case err == nil:
				checkRegionsSafe(t, got, testMemLen, got[0].Base)
			}
		})
	}
}

// TestUffdPageSource: a fault maps to the right page of the right region.
func TestUffdPageSource(t *testing.T) {
	rs := []uffdRegion{
		{Base: testBase, Size: 4 * mib, Offset: 0, PageSize: 4096},
		{Base: testBase + 1<<32, Size: 4 * mib, Offset: 4 * mib, PageSize: 2 * mib},
	}
	for _, tc := range []struct {
		addr, dst, src, size uint64
		ok                   bool
	}{
		{addr: testBase, dst: testBase, src: 0, size: 4096, ok: true},
		{addr: testBase + 4097, dst: testBase + 4096, src: 4096, size: 4096, ok: true},
		{addr: testBase + 4*mib - 1, dst: testBase + 4*mib - 4096, src: 4*mib - 4096, size: 4096, ok: true},
		{addr: testBase + 4*mib},
		{addr: testBase - 1},
		{addr: testBase + 1<<32 + 3*mib, dst: testBase + 1<<32 + 2*mib, src: 6 * mib, size: 2 * mib, ok: true},
		{addr: math.MaxUint64},
		{addr: 0},
	} {
		dst, src, size, ok := pageSource(rs, tc.addr)
		if dst != tc.dst || src != tc.src || size != tc.size || ok != tc.ok {
			t.Errorf("pageSource(%#x) = %#x, %#x, %#x, %v; want %#x, %#x, %#x, %v", tc.addr, dst, src, size, ok, tc.dst, tc.src, tc.size, tc.ok)
		}
	}
}

// checkRegionsSafe asserts what the fault loop and prefetch rely on for a
// table parseRegions accepted: every page they compute is inside the
// memory file and inside its region.
func checkRegionsSafe(t testing.TB, rs []uffdRegion, memLen, addr uint64) {
	t.Helper()
	for _, r := range rs {
		if r.Offset > memLen || r.Size > memLen-r.Offset {
			t.Fatalf("region %+v is past the %d-byte memory file", r, memLen)
		}
		// prefetch copies whole pages from Offset up to Offset+Size.
		if r.PageSize == 0 || r.Size == 0 || r.Size%r.PageSize != 0 || r.Base%r.PageSize != 0 {
			t.Fatalf("region %+v is not whole, aligned pages", r)
		}
		// A fault anywhere in the region, derived from the fuzzed address.
		check(t, rs, r.Base+addr%r.Size, memLen)
	}
	check(t, rs, addr, memLen)
}

func check(t testing.TB, rs []uffdRegion, addr, memLen uint64) {
	t.Helper()
	dst, src, size, ok := pageSource(rs, addr)
	if !ok {
		return
	}
	if src > memLen || size > memLen-src {
		t.Fatalf("fault at %#x reads mem[%#x:+%#x] past the %d-byte memory file (%+v)", addr, src, size, memLen, rs)
	}
	if dst > addr || addr-dst >= size || dst%size != 0 {
		t.Fatalf("fault at %#x fills page %#x+%#x that does not hold it", addr, dst, size)
	}
}

// FuzzRegionTable: no table the VMM can send makes the helper index the
// memory file out of bounds; parseRegions either rejects it or every fault
// and prefetch page it implies is in bounds.
func FuzzRegionTable(f *testing.F) {
	seed := func(rs []uffdRegion, memLen, addr uint64) {
		f.Add(regionsJSON(f, rs...), memLen, addr)
	}
	seed([]uffdRegion{{Base: testBase, Size: testMemLen, PageSize: 4096}}, testMemLen, testBase+5000)
	seed([]uffdRegion{{Base: testBase, Size: 4 * mib, PageSize: 4096}, {Base: testBase + 1<<32, Size: 4 * mib, Offset: 4 * mib, PageSize: 2 * mib}}, testMemLen, testBase+1<<32+3*mib)
	seed([]uffdRegion{{Base: testBase, Size: 2 * 4096, Offset: math.MaxUint64 - 4095, PageSize: 4096}}, testMemLen, testBase)
	seed([]uffdRegion{{Base: testBase + 1, Size: 4096, PageSize: 4096}}, testMemLen, testBase)
	seed([]uffdRegion{{Base: math.MaxUint64 - 4095, Size: 2 * 4096, PageSize: 4096}}, testMemLen, math.MaxUint64)
	f.Add([]byte(`[{"base_host_virt_addr":4096,"size":4096,"offset":0,"page_size":4096}]`), uint64(4096), uint64(4096))
	f.Add([]byte(`[]`), uint64(0), uint64(0))
	f.Add([]byte(`[{}]`), uint64(0), uint64(0))
	f.Fuzz(func(t *testing.T, b []byte, memLen, addr uint64) {
		rs, err := parseRegions(b, memLen)
		if err != nil {
			return
		}
		if len(rs) == 0 || len(rs) > uffdMaxRegions {
			t.Fatalf("accepted %d regions", len(rs))
		}
		checkRegionsSafe(t, rs, memLen, addr)
		if err := validateRegions(rs, memLen); err != nil {
			t.Fatalf("parseRegions accepted a table validateRegions rejects: %v", err)
		}
	})
}
