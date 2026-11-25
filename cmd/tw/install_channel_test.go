package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/npminstall"
)

// One command per install channel (issue #498): tw upgrade only for an
// install.sh prefix.
func TestInstallChannelUpgradeCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   installInfo
		v    string
		want string
	}{
		{installInfo{Channel: channelPrefix, Prefix: "/p"}, "0.6.0", "tw upgrade"},
		{installInfo{Channel: channelNPM}, "0.6.0", "tw backup && tw down && npm install -g tailworks@0.6.0 && tw up"},
		{installInfo{Channel: channelNPM}, "v0.6.0", "tw backup && tw down && npm install -g tailworks@0.6.0 && tw up"},
		{installInfo{Channel: channelNPM}, "", "tw backup && tw down && npm install -g tailworks@latest && tw up"},
		{installInfo{Channel: channelNPX}, "0.6.0", "npx tailworks@latest …"},
		{installInfo{Channel: channelSource, Root: "/src/tw"}, "0.6.0", "cd /src/tw && git pull && make build"},
		{installInfo{Channel: channelOther}, "0.6.0", getShCommand()},
	} {
		if got := c.in.upgradeCommand(c.v); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.in.Channel, c.v, got, c.want)
		}
	}
}

// tw doctor's install row, tw version --check and tw upgrade --plan print
// the same command; on npm the plan is that command, the run refuses.
func TestInstallChannelAgrees(t *testing.T) {
	stubNPMSelf(t, npminstall.Install{Dir: "/g/lib/node_modules/tailworks/node_modules/@tailworks/cli-linux-arm64", Package: "@tailworks/cli-linux-arm64", Version: "0.5.0"}, true)
	if _, d, _ := doctorInstall(); !strings.HasSuffix(d, "; upgrade: "+upgradeCommand("")) {
		t.Fatalf("doctor: %q, want the upgrade command %q", d, upgradeCommand(""))
	}
	state := t.TempDir()
	out, err := captureStdout(t, func() error { return cmdUpgrade([]string{"0.6.0", "--plan", "--state-dir", state}) })
	if err != nil || !strings.Contains(out, upgradeCommand("0.6.0")) || !strings.Contains(out, "0.5.0 → 0.6.0") {
		t.Fatalf("--plan on npm: %v %q", err, out)
	}
	_, err = captureStdout(t, func() error { return cmdUpgrade([]string{"0.6.0", "--yes", "--state-dir", state}) })
	var ce *cliErr
	if !errors.As(err, &ce) || ce.Code != "npm_install" {
		t.Fatalf("run on npm: %v", err)
	}

	stubNPMSelf(t, npminstall.Install{}, false)
	if ch := installChannel(); ch.Channel == channelPrefix || ch.Channel == channelNPM {
		t.Fatalf("a test binary classified as %s", ch.Channel)
	}
	if _, d, _ := doctorInstall(); !strings.HasSuffix(d, "; upgrade: "+upgradeCommand("")) || strings.HasSuffix(d, "tw upgrade") {
		t.Fatalf("doctor (not installed): %q", d)
	}
}
