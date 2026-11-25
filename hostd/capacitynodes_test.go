package hostd

import (
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// The node capacity a host advertises by default is derived from its RAM
// (issue #114): a quarter of it at NodeMemMiB per node, within the bounds.
func TestDefaultCapacityNodes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ mib, want int }{
		{0, FallbackCapacityNodes},
		{-1, FallbackCapacityNodes},
		{1 << 10, MinCapacityNodes}, // 1 GiB: 32 by RAM, raised to the floor
		{4 << 10, 128},
		{8 << 10, 256},
		{16 << 10, 512},
		{32 << 10, MaxCapacityNodes}, // 1024 by RAM, capped
		{256 << 10, MaxCapacityNodes},
	} {
		if got := DefaultCapacityNodes(c.mib); got != c.want {
			t.Errorf("DefaultCapacityNodes(%d MiB) = %d, want %d", c.mib, got, c.want)
		}
	}
}

// New fills an unset CapacityNodes from RAM; an explicit one is kept.
func TestCapacityNodesDefaultFromRAM(t *testing.T) {
	t.Parallel()
	total, _ := memInfo()
	h, _ := admitHost(t, false, mgmtapi.HostCapacity{})
	if got, want := h.cfg.CapacityNodes, DefaultCapacityNodes(total); got != want {
		t.Errorf("default CapacityNodes = %d, want %d (RAM %d MiB)", got, want, total)
	}
	mc, _ := mgmtapi.NewClient("unix:///nonexistent")
	h2, err := New(Config{HostID: "h2", Mgmt: mc, DataDir: t.TempDir(), RunDir: t.TempDir(), WorkerdBin: "/bin/true", CapacityNodes: 7})
	if err != nil {
		t.Fatal(err)
	}
	if h2.cfg.CapacityNodes != 7 {
		t.Errorf("explicit CapacityNodes = %d, want 7", h2.cfg.CapacityNodes)
	}
}
