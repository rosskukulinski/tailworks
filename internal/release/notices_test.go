package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #102: every release tarball carries LICENSE and THIRD_PARTY_NOTICES,
// and one with a microVM guest also the kernel's and the rootfs's
// corresponding-source files. scripts/third-party-notices.mjs writes them
// into a staged tarball; scripts/check-release-notices.sh fails a tarball
// without them.
func TestThirdPartyNotices(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("needs node")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	st := filepath.Join(dir, "tailworks-0.0.0-test-linux-arm64")

	write := func(rel, s string) {
		t.Helper()
		p := filepath.Join(st, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A Go binary (this test's own) and a non-Go one standing in for workerd.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	write("bin/tw", string(b))
	write("bin/workerd", "not a Go binary")
	write("VERSION", "0.0.0-test\n")
	write("share/tailworks/release.json", `{"version":"0.0.0-test","os":"linux","arch":"arm64","workerd":"1.2.3","commit":"0123456789abcdef"}`)
	write("share/tailworks/node_modules/left-pad/package.json", `{"name":"left-pad","version":"1.3.0","license":"WTFPL"}`)
	write("share/tailworks/node_modules/left-pad/LICENSE", "left-pad license\n")
	write("share/tailworks/node_modules/@scope/nolicense/package.json", `{"name":"@scope/nolicense","version":"2.0.0","license":"MIT"}`)
	write("share/tailworks/node_modules/@tailworks/tailnet/package.json", `{"name":"@tailworks/tailnet","version":"0.0.0-test"}`)

	run := func() (string, error) {
		cmd := exec.Command(node, "scripts/third-party-notices.mjs", st)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	tarball := func() string {
		t.Helper()
		tgz := filepath.Join(dir, filepath.Base(st)+".tar.gz")
		cmd := exec.Command("tar", "-czf", tgz, "-C", dir, filepath.Base(st))
		cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tar: %v\n%s", err, out)
		}
		return tgz
	}
	check := func(tgz string) (string, error) {
		cmd := exec.Command("sh", "scripts/check-release-notices.sh", tgz)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// A tarball before the generator ran: the check fails.
	if out, err := check(tarball()); err == nil || !strings.Contains(out, "has no") {
		t.Fatalf("check passed a tarball without LICENSE: %v\n%s", err, out)
	}

	// No guest: LICENSE + THIRD_PARTY_NOTICES only.
	if out, err := run(); err != nil {
		t.Fatalf("third-party-notices: %v\n%s", err, out)
	} else if !strings.Contains(out, "@scope/nolicense@2.0.0") {
		t.Errorf("no warning for a package without a license file:\n%s", out)
	}
	lic, _ := os.ReadFile(filepath.Join(root, "LICENSE"))
	if got, _ := os.ReadFile(filepath.Join(st, "LICENSE")); string(got) != string(lic) {
		t.Error("LICENSE is not the repo's")
	}
	notices, err := os.ReadFile(filepath.Join(st, "THIRD_PARTY_NOTICES"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1. workerd",
		"workerd 1.2.3",
		"Apache License",
		"Go " + strings.TrimPrefix(goVersion(t), "go") + " (the standard library",
		"Copyright 2009 The Go Authors",
		"left-pad@1.3.0: WTFPL\n  share/tailworks/node_modules/left-pad/LICENSE",
		"@scope/nolicense@2.0.0: MIT",
	} {
		if !strings.Contains(string(notices), want) {
			t.Errorf("THIRD_PARTY_NOTICES lacks %q", want)
		}
	}
	for _, not := range []string{"@tailworks/tailnet@", "GNU GENERAL PUBLIC LICENSE"} {
		if strings.Contains(string(notices), not) {
			t.Errorf("THIRD_PARTY_NOTICES (no guest) has %q", not)
		}
	}
	if out, err := check(tarball()); err != nil {
		t.Fatalf("check-release-notices: %v\n%s", err, out)
	}

	// With a microVM guest whose manifest has no package list: the
	// generator fails (an old guest build can't be released).
	write("share/tailworks/vm/vmlinux", "kernel")
	write("share/tailworks/vm/rootfs.ext4", "rootfs")
	write("share/tailworks/vm/firecracker", "fc")
	write("share/tailworks/vm/jailer", "jailer")
	write("share/tailworks/vm/kernel.json", `{"arch":"arm64","kernel_version":"6.1.155","source_sha256":"aaaa","base_config_sha256":"bbbb","fragment_sha256":"cccc","config_sha256":"dddd","compiler":"gcc 13","source_date_epoch":1}`)
	write("share/tailworks/vm/manifest.json", `{"arch":"arm64","workerd_version":"1.2.3"}`)
	if out, err := run(); err == nil || !strings.Contains(out, `no "packages"`) {
		t.Fatalf("third-party-notices accepted a guest without a package list: %v\n%s", err, out)
	}
	// The check also fails: there is a vmlinux but no KERNEL-SOURCE.md.
	if out, err := check(tarball()); err == nil || !strings.Contains(out, "KERNEL-SOURCE.md") {
		t.Fatalf("check passed a guest without KERNEL-SOURCE.md: %v\n%s", err, out)
	}

	pkgs := []map[string]any{{
		"package": "libc6", "version": "2.39-0ubuntu8.8", "source": "glibc", "source_version": "2.39-0ubuntu8.8",
		"files": []string{"/usr/lib/aarch64-linux-gnu/libc.so.6"},
	}}
	m, _ := json.Marshal(map[string]any{"arch": "arm64", "workerd_version": "1.2.3", "packages": pkgs})
	write("share/tailworks/vm/manifest.json", string(m))
	if out, err := run(); err != nil {
		t.Fatalf("third-party-notices (guest): %v\n%s", err, out)
	}
	ks, _ := os.ReadFile(filepath.Join(st, "share/tailworks/vm/KERNEL-SOURCE.md"))
	rs, _ := os.ReadFile(filepath.Join(st, "share/tailworks/vm/ROOTFS-SOURCE.md"))
	notices, _ = os.ReadFile(filepath.Join(st, "THIRD_PARTY_NOTICES"))
	for _, c := range []struct{ name, text, want string }{
		{"KERNEL-SOURCE.md", string(ks), "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.155.tar.xz"},
		{"KERNEL-SOURCE.md", string(ks), "/0123456789abcdef/guest/kernel/tw.config"},
		{"KERNEL-SOURCE.md", string(ks), "Written offer"},
		{"ROOTFS-SOURCE.md", string(rs), "https://launchpad.net/ubuntu/+source/glibc/2.39-0ubuntu8.8"},
		{"ROOTFS-SOURCE.md", string(rs), "/usr/share/doc/libc6/copyright"},
		{"ROOTFS-SOURCE.md", string(rs), "Written offer"},
		{"THIRD_PARTY_NOTICES", string(notices), "libc6 2.39-0ubuntu8.8 (source: glibc 2.39-0ubuntu8.8)"},
		{"THIRD_PARTY_NOTICES", string(notices), "Firecracker\nCopyright"},
		{"THIRD_PARTY_NOTICES", string(notices), "GNU GENERAL PUBLIC LICENSE"},
		{"THIRD_PARTY_NOTICES", string(notices), "GNU LESSER GENERAL PUBLIC LICENSE"},
		{"THIRD_PARTY_NOTICES", string(notices), "Used by: tw-guestd (in share/tailworks/vm/rootfs.ext4)"},
	} {
		if !strings.Contains(c.text, c.want) {
			t.Errorf("%s lacks %q", c.name, c.want)
		}
	}
	if out, err := check(tarball()); err != nil {
		t.Fatalf("check-release-notices (guest): %v\n%s", err, out)
	}
}

func goVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
