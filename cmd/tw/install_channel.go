package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/catalog"
	"github.com/rosskukulinski/tailworks/internal/npminstall"
)

// How this tailworks was installed decides how it is upgraded (issue #498).
// tw version --check, tw doctor and tw upgrade --plan all ask
// installChannel, so they print the same one command.

// channel is how this tailworks was installed.
type channel string

const (
	channelPrefix channel = "install.sh" // a release tarball's install.sh prefix: tw upgrade
	channelNPM    channel = "npm"        // npm install -g tailworks
	channelNPX    channel = "npx"        // npx tailworks (npm exec's cache)
	channelSource channel = "source"     // a source checkout: make build
	channelOther  channel = "other"      // a binary copied somewhere by hand
)

// installInfo is this tailworks' install channel and where it lives.
type installInfo struct {
	Channel channel
	// Prefix is the install.sh prefix (channelPrefix).
	Prefix string
	// NPM is the platform package (channelNPM, channelNPX).
	NPM npminstall.Install
	// Root is the source checkout (channelSource).
	Root string
}

// installChannel classifies the tailworks binary that is running.
func installChannel() installInfo {
	if in, ok := npmSelf(); ok { // npm_install.go
		if in.NPX {
			return installInfo{Channel: channelNPX, NPM: in}
		}
		return installInfo{Channel: channelNPM, NPM: in}
	}
	if p := installPrefixOf(); p != "" { // upgrade.go
		return installInfo{Channel: channelPrefix, Prefix: p}
	}
	if root := sourceCheckout(); root != "" { // sourcehints.go
		return installInfo{Channel: channelSource, Root: root}
	}
	return installInfo{Channel: channelOther}
}

// getShCommand installs the latest release tarball (docs/INSTALL.md).
func getShCommand() string {
	return "curl -fsSLO https://raw.githubusercontent.com/" + releaseRepo() + "/master/scripts/get.sh && sh get.sh"
}

// upgradeCommand is the one command that upgrades this install to release
// v ("" or "latest": the newest).
func (i installInfo) upgradeCommand(v string) string {
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		v = "latest"
	}
	switch i.Channel {
	case channelPrefix:
		return "tw upgrade"
	case channelNPM:
		// npm has no signature check or rollback: back up first.
		return "tw backup && tw down && npm install -g tailworks@" + v + " && tw up"
	case channelNPX:
		return "npx tailworks@latest …"
	case channelSource:
		return "cd " + i.Root + " && git pull && make build"
	}
	return getShCommand()
}

// upgradeCommand is version --check's upgrade hint for release latest.
func upgradeCommand(latest string) string { return installChannel().upgradeCommand(latest) }

// npmUpgradePlan is tw upgrade --plan on an npm install: npm owns the
// install, so the plan is the npm command (the run itself still refuses
// with npm_install).
func npmUpgradePlan(f *flags, ch installInfo, want string) error {
	v := want
	if v == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if tag, err := latestTag(ctx, cmpOr(os.Getenv("TW_REPO"), "rosskukulinski/tailworks")); err == nil {
			v = strings.TrimPrefix(tag, "v")
		}
	}
	cmd := ch.upgradeCommand(v)
	p := localPlan{From: cmpOr(ch.NPM.Version, version), To: cmpOr(v, "latest"), Source: ch.NPM.Kind() + " (" + ch.NPM.Dir + ")",
		Breaking: []catalog.Note{}, Actions: []catalog.Note{}, ContractMigrations: []string{},
		Steps: []string{cmd},
		Warnings: []string{"npm owns this install, so tw upgrade won't run it: run the command above (npm has no signature check or rollback; tw backup keeps a way back)",
			"check the release notes for contract migrations and breaking changes first"}}
	emit(f, localResult{Plan: p}, func() {
		fmt.Printf("Upgrade tailworks %s → %s (%s)\n", p.From, p.To, p.Source)
		fmt.Println("  " + cmd)
		for _, w := range p.Warnings {
			fmt.Println("  ! " + w)
		}
	})
	return nil
}

// noPrefixUpgrade is tw upgrade's hint when this tailworks doesn't run
// from an install.sh prefix (a source checkout or a hand-copied binary).
func noPrefixUpgrade(want string) string {
	return "this tailworks isn't an install.sh prefix, so tw upgrade can't replace it: upgrade with " + installChannel().upgradeCommand(want)
}
