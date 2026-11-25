package hostd

import "testing"

// TestWorkerInternetMode: StatusView.WorkerInternet (tw status, tw doctor)
// names the host's Worker internet path.
func TestWorkerInternetMode(t *testing.T) {
	t.Parallel()
	if m := workerInternetMode(nil); m != "off" {
		t.Errorf("nil: %q", m)
	}
	if m := workerInternetMode(DirectInternet()); m != "direct" {
		t.Errorf("DirectInternet: %q", m)
	}
}
