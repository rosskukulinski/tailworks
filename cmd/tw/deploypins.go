package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/registry"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// deployImage is one image a deployment's container class was pinned to
// at deploy (ADR-032 D3): tw deploy's images[] and its ready event.
type deployImage struct {
	Class string `json:"class"`
	container.Pin
}

// deployImages lists the deployment's pins, by class, name and arch.
func deployImages(v mgmt.DeploymentView) []deployImage {
	var out []deployImage
	for _, c := range v.Containers {
		for _, p := range c.Pins {
			out = append(out, deployImage{Class: c.Class, Pin: p})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Arch < b.Arch
	})
	return out
}

// printDeployImages prints one "✓ image" line per pin, so a registry ref
// shows what mgmt resolved it to (alias, public mirror or upstream digest).
// Images this deploy built already got their line from
// buildContainerImages (images.go), so they are skipped.
func printDeployImages(w io.Writer, imgs []deployImage, built map[string]registry.Built) {
	skip := map[string]bool{}
	for _, b := range built {
		if b.Image != "" {
			skip[b.Image] = true
		}
		for _, r := range b.Images {
			skip[r] = true
		}
	}
	for _, im := range imgs {
		if skip[im.Ref] {
			continue
		}
		label := im.Class
		if im.Name != "" {
			label += ".images." + im.Name
		}
		line := fmt.Sprintf("✓ image      %s %s linux/%s → %s@%s", tsafe(label), tsafe(im.Ref), tsafe(im.Arch), tsafe(im.Repository), tsafe(im.Digest))
		if im.From != "" && im.From != im.Ref {
			line += " (" + tsafe(im.From) + ")"
		}
		fmt.Fprintln(w, line)
	}
}
