package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorSummary(t *testing.T) {
	t.Parallel()
	checks := []check{
		{Name: "binaries", Status: "info", Detail: "not found next to tailworks (/x) or on PATH: tw-mgmt, tw-hostd; not needed in single-process mode (the daemons run inside tailworks), only for tw up --separate-host"},
		{Name: "workerd", Status: "warn", Detail: "/home/u/.tailworks/bin/workerd: workerd 2026-11-14 (tailworks is tested with 1.20261121.1)"},
		{Name: "bundler", Status: "fail", Detail: "bundling template agent failed", Fix: "cd /home/u/src && make deps"},
		{Name: "mint_mode", Status: "ok", Detail: "oauth: OAuth client abc from /home/u/oauth.env mints every node key"},
		{Name: "transport", Status: "ok", Detail: "tsnet: mgmt API on its own node m.example.ts.net (WhoIs); host embedded"},
		{Name: "data_disk", Status: "ok", Detail: "459.2 GiB free of 926.3 GiB on the volume holding /home/u/.tailworks; mgmt.db 14.3 MiB"},
		{Name: "sandbox", Status: "ok", Detail: "Seatbelt works on macOS 27.2: workerd starts under a tailworks profile"},
		{Name: "host_sockets", Status: "skip", Detail: "Linux-only check"},
		{Name: "public_probe:hello", Status: "ok", Detail: "reachable"},
	}
	var b bytes.Buffer
	renderDoctorSummary(&b, checks, "/home/u", 100)
	out := b.String()
	for _, want := range []string{
		"✗ 1 failed · ! 1 warning · ✓ 6 passed · 1 skipped\n",
		"✗ bundler  bundling template agent failed\n",
		"fix: cd ~/src && make deps\n",
		"! workerd  ~/.tailworks/bin/workerd: workerd 2026-11-14",
		"✓ Install    daemon binaries (in-process)\n",
		"✓ Tailnet    mint (oauth) · transport (tsnet) · public hello\n",
		"✓ Storage    disk (459.2 GiB free)\n",
		"✓ Isolation  sandbox (Seatbelt)\n",
		"tw doctor -v",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Linux-only") || strings.Contains(out, "/home/u/") {
		t.Errorf("summary shows a skipped check's detail or an unshortened home dir:\n%s", out)
	}
	// Failures come before warnings, and problems before the passing groups.
	if i, j, k := strings.Index(out, "✗ bundler"), strings.Index(out, "! workerd"), strings.Index(out, "✓ Install"); !(i < j && j < k) {
		t.Errorf("order: bundler %d, workerd %d, Install %d:\n%s", i, j, k, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if n := len([]rune(l)); n > 100 {
			t.Errorf("line is %d wide (> 100): %q", n, l)
		}
	}
}

func TestDoctorChecksHaveGroups(t *testing.T) {
	t.Parallel()
	// Every check doctor runs on some platform has a group and label, so
	// none falls into "Other" with its raw name.
	for _, n := range []string{"api_key", "binaries", "bundler", "cgroups_v2",
		"containers",
		"data_disk", "firecracker", "glibc", "host_sockets",
		"https_certs", "kernel", "kvm", "landlock", "microvm_tier", "mint_mode", "node_egress", "public_ingress", "sandbox",
		"side_channels",
		"state_dir_writable", "store_sizes", "system_tailscaled_not_used", "systemd", "transport", "tw_home_writable",
		"user_namespaces", "worker_internet", "workerd",
		"workerd_age",
		"funnel_attr:x", "public_probe:x"} {
		if g := metaOf(n).group; g == "Other" {
			t.Errorf("check %s has no group", n)
		}
	}
}

func TestWrapText(t *testing.T) {
	t.Parallel()
	got := wrapText("aaa bbb ccc ddd eee fff ggg hhh iii jjj kkk", 20)
	if len(got) != 3 || got[0] != "aaa bbb ccc ddd eee" {
		t.Fatalf("wrapText: %q", got)
	}
	if got := wrapText("", 40); len(got) != 1 || got[0] != "" {
		t.Fatalf("wrapText empty: %q", got)
	}
}
