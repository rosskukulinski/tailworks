//go:build e2evm && (linux || darwin)

package vm

import (
	"fmt"
	"testing"
	"time"
)

// TestE2EVMAlarms: Durable Object alarms on the microVM tier
// (e2e/vm/testdata/vmalarm), in both object modes: a facet's alarm is
// delegated to its Host DO, a plain-DO object uses workerd's own. Covers
// setAlarm firing alarm() and clearing the pending alarm, and an alarm set
// before a VM kill and due after it firing on the new VM (workerd's alarm
// state is on the encrypted volume).
func TestE2EVMAlarms(t *testing.T) {
	s := startVMSuite(t, nil)
	base := s.deploy(vmApp("vmalarm"), "vmalarm", nil)
	state := func(ns, name string) map[string]any {
		_, v := s.do("GET", fmt.Sprintf("%s/%s/%s/", base, ns, name), nil)
		m, _ := v.(map[string]any)
		return m
	}
	waitAlarms := func(ns, name string, n float64, timeout time.Duration) map[string]any {
		t.Helper()
		var m map[string]any
		s.eventually(timeout, fmt.Sprintf("%s/%s: %v alarm runs", ns, name, n), func() bool {
			m = state(ns, name)
			return m["alarms"] == n
		})
		return m
	}

	for _, ns := range []string{"CLOCK", "PLAIN"} {
		t0 := time.Now()
		if m := s.must("GET", base+"/"+ns+"/a/arm?ms=200", nil); m["pending"] == nil {
			t.Fatalf("%s: getAlarm after setAlarm: %v", ns, m)
		}
		m := waitAlarms(ns, "a", 1, 30*time.Second)
		if m["pending"] != nil {
			t.Fatalf("%s: alarm still pending after it ran: %v", ns, m)
		}
		t.Logf("%s: alarm ran %v after setAlarm(+200 ms)", ns, time.Since(t0).Round(time.Millisecond))
		s.must("GET", base+"/"+ns+"/a/arm?ms=100", nil)
		waitAlarms(ns, "a", 2, 30*time.Second)
	}

	// Alarms set before a guest crash fire on the new VM.
	for _, ns := range []string{"CLOCK", "PLAIN"} {
		s.must("GET", base+"/"+ns+"/k/arm?ms=6000", nil)
	}
	s.killVM("vmalarm", base+"/PLAIN/ready/")
	for _, ns := range []string{"CLOCK", "PLAIN"} {
		m := waitAlarms(ns, "k", 1, 60*time.Second)
		t.Logf("%s: the alarm set before the VM kill ran on the new VM: %v", ns, m)
	}
}
