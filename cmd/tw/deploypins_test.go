package main

import (
	"bytes"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/registry"
	"github.com/rosskukulinski/tailworks/mgmt"
)

func TestDeployImagesLines(t *testing.T) {
	v := mgmt.DeploymentView{Containers: []container.Class{
		{Class: "Sandbox", Pins: []container.Pin{
			{Ref: "docker.io/cloudflare/sandbox:0.12.14", Arch: "arm64", Repository: "public/docker.io/cloudflare/sandbox", Digest: "sha256:aa", From: "alias tw.local/cloudflare-sandbox:0.12.14-arm64"},
		}},
		{Class: "Built", Pins: []container.Pin{
			{Ref: "tw.local/tn_1/app-built:abc", Arch: "amd64", Repository: "tn_1/app-built", Digest: "sha256:bb", From: "tw.local/tn_1/app-built:abc"},
		}},
		{Class: "Api", Pins: []container.Pin{
			{Name: "tool", Ref: "tw.local/public/tool:1", Arch: "amd64", Repository: "public/tool", Digest: "sha256:cc", From: "tw.local/public/tool:1"},
			{Ref: "nginx:1", Arch: "amd64", Repository: "tn_1/mirror/docker.io/library/nginx", Digest: "sha256:dd", From: "docker.io/library/nginx:1@sha256:ee"},
		}},
	}}
	imgs := deployImages(v)
	if len(imgs) != 4 || imgs[0].Class != "Api" || imgs[0].Name != "" || imgs[1].Name != "tool" || imgs[3].Class != "Sandbox" {
		t.Fatalf("deployImages order: %+v", imgs)
	}
	var b bytes.Buffer
	printDeployImages(&b, imgs, map[string]registry.Built{"Built": {Image: "tw.local/tn_1/app-built:abc"}})
	want := "✓ image      Api nginx:1 linux/amd64 → tn_1/mirror/docker.io/library/nginx@sha256:dd (docker.io/library/nginx:1@sha256:ee)\n" +
		"✓ image      Api.images.tool tw.local/public/tool:1 linux/amd64 → public/tool@sha256:cc\n" +
		"✓ image      Sandbox docker.io/cloudflare/sandbox:0.12.14 linux/arm64 → public/docker.io/cloudflare/sandbox@sha256:aa (alias tw.local/cloudflare-sandbox:0.12.14-arm64)\n"
	if got := b.String(); got != want {
		t.Fatalf("lines:\n%s\nwant:\n%s", got, want)
	}
	b.Reset()
	printDeployImages(&b, deployImages(mgmt.DeploymentView{}), nil)
	if b.Len() != 0 {
		t.Fatalf("no containers printed %q", b.String())
	}
}
