//go:build linux

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
)

// doctorSideChannels (Linux) reports the host's CPU side-channel posture
// from the live sysfs and /proc and, while tailworks runs, what this host
// serves that makes it matter (tenants, Node-tier workloads, the microVM
// tier). Reading these files needs no privilege; the process-tier sandbox
// lets workloads read /sys/devices/system/cpu too, so nothing here is new
// to a tenant.
func doctorSideChannels(s *state) (string, string, string) {
	st, d, fix := readSideChannels("/").report()
	if s == nil || !s.configured() || !s.supervisorAlive() {
		return st, d, fix
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hv hostd.StatusView
	if err := s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv); err != nil {
		return st, d, fix
	}
	tenants := map[string]bool{}
	nodeApps := 0
	for _, w := range hv.Workloads {
		tenants[w.TenantID] = true
		if w.Runtime == "node" {
			nodeApps++
		}
	}
	var now []string
	if len(tenants) > 1 {
		now = append(now, fmt.Sprintf("%d tenants", len(tenants)))
	}
	if nodeApps > 0 {
		now = append(now, fmt.Sprintf("%d Node-runtime workloads", nodeApps))
	}
	if strings.Contains(strings.Join(hv.Tiers, ","), "microvm") {
		now = append(now, "the microVM tier")
	}
	if len(now) > 0 {
		d += "; this host now serves " + strings.Join(now, ", ")
	}
	return st, d, fix
}
