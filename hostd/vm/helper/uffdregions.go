package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// The uffd region table, as Firecracker sends it in the uffd handshake.
// It comes from the jailed VMM, which is less trusted than the root
// helper (a tenant that escaped its guest into Firecracker writes it), so
// every field is checked before the fault loop indexes the base memory
// file with it (security/LEDGER.md H11). This file has no build tag so the
// checks are unit- and fuzz-tested on every OS; the fault loop itself is
// in uffd_linux.go.

// GuestRegionUffdMapping (src/firecracker/examples/uffd/uffd_utils.rs).
type uffdRegion struct {
	Base     uint64 `json:"base_host_virt_addr"`
	Size     uint64 `json:"size"`
	Offset   uint64 `json:"offset"`
	PageSize uint64 `json:"page_size"`
}

const (
	// uffdMinPageSize is the smallest page UFFDIO_COPY copies, and the
	// alignment the kernel requires of its source address (the memory file
	// is mapped page-aligned, so the file offset must be 4 KiB aligned).
	uffdMinPageSize = 4096
	// uffdMaxPageSize: 1 GiB hugetlbfs pages are the largest Linux has.
	uffdMaxPageSize = 1 << 30
	// uffdMaxRegions bounds the table. Firecracker sends one region per
	// guest memory slot: one or two on x86_64 (the gap below 4 GiB), one on
	// aarch64.
	uffdMaxRegions = 64
)

// parseRegions decodes and validates the region table from the uffd
// handshake against a memory file of memLen bytes.
func parseRegions(b []byte, memLen uint64) ([]uffdRegion, error) {
	var regions []uffdRegion
	if err := json.Unmarshal(b, &regions); err != nil {
		return nil, fmt.Errorf("uffd handshake: bad mappings: %w", err)
	}
	if err := validateRegions(regions, memLen); err != nil {
		return nil, err
	}
	return regions, nil
}

// validateRegions checks that every address the fault loop and prefetch
// compute from the table stays inside the memory file and inside one
// region: page sizes are sane powers of two, bases and sizes are whole
// pages, no sum wraps, every region's file range is in the file, and no two
// regions overlap in guest memory.
func validateRegions(regions []uffdRegion, memLen uint64) error {
	if len(regions) == 0 {
		return errors.New("uffd: empty region table")
	}
	if len(regions) > uffdMaxRegions {
		return fmt.Errorf("uffd: %d regions (at most %d)", len(regions), uffdMaxRegions)
	}
	for i, r := range regions {
		bad := func(why string) error {
			return fmt.Errorf("uffd: bad region %d %+v (memory file %d bytes): %s", i, r, memLen, why)
		}
		switch {
		case r.PageSize < uffdMinPageSize || r.PageSize > uffdMaxPageSize || r.PageSize&(r.PageSize-1) != 0:
			return bad("page size is not a power of two between 4 KiB and 1 GiB")
		case r.Base%r.PageSize != 0:
			return bad("base is not page aligned")
		case r.Size == 0 || r.Size%r.PageSize != 0:
			return bad("size is not a whole number of pages")
		case r.Size > math.MaxUint64-r.Base:
			return bad("base + size wraps")
		case r.Offset%uffdMinPageSize != 0:
			return bad("offset is not 4 KiB aligned")
		case r.Offset > memLen || r.Size > memLen-r.Offset:
			return bad("offset + size is past the end of the memory file")
		}
	}
	sorted := append([]uffdRegion(nil), regions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Base < sorted[j].Base })
	for i := 1; i < len(sorted); i++ {
		if p, r := sorted[i-1], sorted[i]; r.Base < p.Base+p.Size {
			return fmt.Errorf("uffd: regions %+v and %+v overlap", p, r)
		}
	}
	return nil
}

// pageSource finds the region holding a faulting guest address and returns
// the page to fill (dst), its offset in the memory file (src) and its size.
// For a table that passed validateRegions, src+size <= len(memory file).
func pageSource(regions []uffdRegion, addr uint64) (dst, src, size uint64, ok bool) {
	for _, r := range regions {
		if addr < r.Base || addr-r.Base >= r.Size {
			continue
		}
		dst = addr &^ (r.PageSize - 1)
		return dst, r.Offset + (dst - r.Base), r.PageSize, true
	}
	return 0, 0, 0, false
}
