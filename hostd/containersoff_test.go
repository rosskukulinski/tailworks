package hostd

import "testing"

// TestContainersOff: tw doctor's reason. No microVM backend says so; with
// one, the reason tw-hostd found at startup.
func TestContainersOff(t *testing.T) {
	t.Parallel()
	h := &Host{}
	if got := h.containersOff(); got != "needs the microVM tier" {
		t.Errorf("no VM: %q", got)
	}
	h.cfg.ContainersOff = "mkfs.erofs not found (install erofs-utils)"
	if got := h.containersOff(); got != "needs the microVM tier" {
		t.Errorf("no VM, a reason: %q", got)
	}
	if got := h.Status().ContainersOff; got != "needs the microVM tier" {
		t.Errorf("StatusView: %q", got)
	}
	h.cfg.VM = fakeBackend{}
	if got := h.containersOff(); got != "mkfs.erofs not found (install erofs-utils)" {
		t.Errorf("VM, a reason: %q", got)
	}
	h.cfg.ContainersOff = ""
	if got := h.containersOff(); got != "no container backend" {
		t.Errorf("VM, no reason: %q", got)
	}
}
