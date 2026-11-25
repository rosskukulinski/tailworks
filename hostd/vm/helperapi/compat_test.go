package helperapi

import "testing"

func TestRangesOverlap(t *testing.T) {
	for _, c := range []struct {
		min, max, pmin, pmax int
		ok                   bool
	}{
		{2, 2, 2, 2, true},
		{2, 2, 0, 2, true},  // old helper: no minVersion, speaks 2
		{2, 2, 0, 1, false}, // old helper speaking 1
		{2, 3, 0, 2, true},  // newer hostd still serves 2
		{3, 4, 2, 2, false}, // hostd dropped 2
		{2, 2, 2, 3, true},  // helper upgraded first, still serves 2
		{2, 2, 3, 4, false}, // helper dropped 2
		{2, 2, 5, 3, false}, // min > max is ignored: speaks 3 only
	} {
		if got := rangesOverlap(c.min, c.max, c.pmin, c.pmax); got != c.ok {
			t.Errorf("rangesOverlap(%d,%d,%d,%d) = %v, want %v", c.min, c.max, c.pmin, c.pmax, got, c.ok)
		}
	}
	if err := Check(Response{Version: Version, MinVersion: MinVersion}); err != nil {
		t.Error(err)
	}
	if err := Check(Response{Version: Version}); err != nil {
		t.Error(err)
	}
	if err := Check(Response{Version: 1}); err == nil {
		t.Error("protocol 1 helper accepted")
	}
}
