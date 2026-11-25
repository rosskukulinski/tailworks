package mgmt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/container"
	"github.com/rosskukulinski/tailworks/internal/cron"
	"github.com/rosskukulinski/tailworks/internal/publicspec"
	"github.com/tailscale/hujson"
)

// TwConfig is the part of tailworks.jsonc mgmt reads.
type TwConfig struct {
	// Tier is the requested isolation tier ("process", "microvm"; "" = the
	// tenant's default). Placement only sends a workload to hosts that offer
	// its tier.
	Tier string `json:"tier"`
	// Runtime is "" / "workerd" or "node" (Node apps always run in the
	// process tier; public ingress is workerd-only in v1).
	Runtime string `json:"runtime"`
	// Public is tailworks.jsonc "public" (ADR-029): path prefixes reachable from
	// the public internet over Funnel. nil: tailnet-only.
	Public *publicspec.Spec `json:"public"`
	// Placement is smart placement (ADR-033): mode "smart" (default) or
	// "off"; targets default to tailnet.connects. Region/Host/Hostname are
	// Cloudflare's hints: host/hostname name one target, region is ignored.
	Placement struct {
		Mode      string   `json:"mode"`
		Targets   []string `json:"targets"`
		Aggregate string   `json:"aggregate"`
		Region    string   `json:"region"`
		Host      string   `json:"host"`
		Hostname  string   `json:"hostname"`
	} `json:"placement"`

	// Replicas runs a stateless workload on this many distinct hosts
	// (default 1); see workload_replicas.go.
	Replicas int `json:"replicas"`

	Tailnet struct {
		// Tags are the workload entry node's tags (the "_workload" namespace).
		Tags []string `json:"tags"`
		// Service: true ("svc:<workload>"), or a Service name the workload
		// entry node hosts (Tailscale Services); see ServiceName.
		Service json.RawMessage `json:"service"`
		// Connects are declared dependencies (tags or names), preflighted by tw deploy.
		Connects []string `json:"connects"`
		Objects  map[string]struct {
			Tags   []string `json:"tags"`
			TTL    string   `json:"ttl"`
			Node   *bool    `json:"node"`   // false: internal class, no tailnet node
			Egress string   `json:"egress"` // "workload": plain-DO mode
			// Fence "never": instances of this class tolerate running on two
			// hosts at once, so mgmt may move it off an unreachable host
			// without --force-takeover (ADR-028 §3).
			Fence string `json:"fence"`
			// Public lets public requests create objects of this class (ADR-029 D5).
			Public *publicspec.ClassSpec `json:"public"`
		} `json:"objects"`
		Spawn struct {
			MaxLive  int `json:"maxLive"`
			MaxDepth int `json:"maxDepth"`
		} `json:"spawn"`

		// HTTPS is tailnet.https: the workload node also serves :443, so
		// the caller grant opens tcp:443 too (callergrant_https.go).
		HTTPS bool `json:"https"`
	} `json:"tailnet"`
}

// WorkloadNamespace is the object namespace of a workload's entry node
// (its default export); the object name is the workload name.
const WorkloadNamespace = "_workload"

// Classes returns the DO namespaces that get tailnet nodes, sorted by
// class, plus the implicit "_workload" namespace (tailnet.tags) when set.
// Classes with "node": false have no identity and are not namespaces.
func (c *TwConfig) Classes() []ClassConfig {
	var out []ClassConfig
	for name, o := range c.Tailnet.Objects {
		if o.Node != nil && !*o.Node {
			continue
		}
		out = append(out, ClassConfig{Class: name, Tags: o.Tags, TTL: o.TTL})
	}
	if len(c.Tailnet.Tags) > 0 {
		out = append(out, ClassConfig{Class: WorkloadNamespace, Tags: c.Tailnet.Tags})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// PublicConfig validates tailworks.jsonc "public" and the per-class public
// settings; nil when the workload declares no public routes.
func (c *TwConfig) PublicConfig() (*DeploymentPublic, error) {
	var classes map[string]publicspec.ClassSpec
	for name, o := range c.Tailnet.Objects {
		if o.Public == nil {
			continue
		}
		if err := o.Public.Validate(name); err != nil {
			return nil, err
		}
		if c.Public == nil && o.Public.Create {
			return nil, fmt.Errorf("tailnet.objects.%s.public.create needs tailworks.jsonc \"public\" routes", name)
		}
		if classes == nil {
			classes = map[string]publicspec.ClassSpec{}
		}
		classes[name] = *o.Public
	}
	if c.Public == nil {
		return nil, nil
	}
	if c.Runtime == "node" {
		return nil, errors.New("\"public\" is not supported for runtime \"node\" yet (ADR-029 v1: workerd workloads only)")
	}
	sp := *c.Public
	if err := sp.Validate(); err != nil {
		return nil, err
	}
	return &DeploymentPublic{Spec: sp, Classes: classes}, nil
}

// ParseTwConfig parses tailworks.jsonc (JSONC/HuJSON).
func ParseTwConfig(b []byte) (*TwConfig, error) {
	// Standardize rewrites its argument in place (comments become spaces);
	// copy so the caller's bytes (tailworks.jsonc as shipped) keep their comments.
	std, err := hujson.Standardize(append([]byte(nil), b...))
	if err != nil {
		return nil, fmt.Errorf("tailworks.jsonc: %w", err)
	}
	var c TwConfig
	if err := json.Unmarshal(std, &c); err != nil {
		return nil, fmt.Errorf("tailworks.jsonc: %w", err)
	}
	return &c, nil
}

// BundleConfig extracts tailworks.jsonc from a (optionally gzipped) tar bundle,
// preferring the shallowest one.
// BundleStateful reports whether moving this workload between hosts can
// split its Durable Object state (ADR-028 §3): true if any DO class, from
// tailworks.jsonc tailnet.objects or wrangler.jsonc/.json durable_objects, is not
// marked "fence": "never". It errs on the side of stateful: a wrangler.toml
// (not parsed here) or an unreadable bundle counts as stateful. reason names
// the classes or the uncertainty.
func BundleStateful(bundle []byte) (stateful bool, reason string) {
	files, err := bundleFiles(bundle, "tailworks.jsonc", "wrangler.jsonc", "wrangler.json", "wrangler.toml")
	if err != nil {
		return true, "bundle unreadable"
	}
	exempt := map[string]bool{}
	classes := map[string]bool{}
	if b, ok := files["tailworks.jsonc"]; ok {
		if cfg, err := ParseTwConfig(b); err == nil {
			for name, o := range cfg.Tailnet.Objects {
				classes[name] = true
				if o.Fence == "never" {
					exempt[name] = true
				}
			}
		} else {
			return true, "tailworks.jsonc unreadable"
		}
	}
	for _, f := range []string{"wrangler.jsonc", "wrangler.json"} {
		b, ok := files[f]
		if !ok {
			continue
		}
		std, err := hujson.Standardize(b)
		var w struct {
			DurableObjects struct {
				Bindings []struct {
					ClassName string `json:"class_name"`
				} `json:"bindings"`
			} `json:"durable_objects"`
			// Workflows (ADR-040) keep their instances in the workload's
			// DO storage on its host: stateful like a DO class.
			Workflows []struct {
				Name string `json:"name"`
			} `json:"workflows"`
		}
		if err != nil || json.Unmarshal(std, &w) != nil {
			return true, f + " unreadable"
		}
		for _, b := range w.DurableObjects.Bindings {
			if b.ClassName != "" {
				classes[b.ClassName] = true
			}
		}
		for _, wf := range w.Workflows {
			classes["workflow "+wf.Name] = true
		}
	}
	if _, ok := files["wrangler.toml"]; ok {
		return true, "wrangler.toml (not inspected; assumed to declare Durable Objects)"
	}
	var guarded []string
	for c := range classes {
		if !exempt[c] {
			guarded = append(guarded, c)
		}
	}
	sort.Strings(guarded)
	if len(guarded) == 0 {
		return false, ""
	}
	return true, "Durable Object classes " + strings.Join(guarded, ", ")
}

// BundleCrons returns the bundle's cron triggers (wrangler.jsonc or
// wrangler.json "triggers": {"crons": [...]}), validated: at most
// cron.MaxTriggers, no duplicates, each a valid expression. A bundle with no
// wrangler config, or no triggers, has none. Hosts re-validate on deploy.
func BundleCrons(bundle []byte) ([]string, error) {
	files, err := bundleFiles(bundle, "wrangler.jsonc", "wrangler.json")
	if err != nil {
		return nil, err
	}
	for _, f := range []string{"wrangler.jsonc", "wrangler.json"} {
		b, ok := files[f]
		if !ok {
			continue
		}
		std, err := hujson.Standardize(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		var w struct {
			Triggers struct {
				Crons []string `json:"crons"`
			} `json:"triggers"`
		}
		if err := json.Unmarshal(std, &w); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if err := cron.ValidateTriggers(w.Triggers.Crons); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		return w.Triggers.Crons, nil
	}
	return nil, nil
}

// bundleFiles returns the shallowest copy of each named file (by base name)
// in a tar or tar.gz bundle.
func bundleFiles(bundle []byte, names ...string) (map[string][]byte, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var r io.Reader = bytes.NewReader(bundle)
	if len(bundle) > 2 && bundle[0] == 0x1f && bundle[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		r = gz
	}
	tr := tar.NewReader(r)
	out, depth := map[string][]byte{}, map[string]int{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		base := path.Base(name)
		if h.Typeflag != tar.TypeReg || !want[base] || strings.Contains(name, "node_modules/") {
			continue
		}
		d := strings.Count(name, "/")
		if old, ok := depth[base]; ok && old <= d {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		out[base], depth[base] = b, d
	}
}

// BundleContainers returns the container classes a bundle declares: the
// wrangler.jsonc/.json "containers" array (Cloudflare's shape) with
// tailworks.jsonc "containers" overriding by class (ADR-032). A bundle that
// declares containers needs a host offering them (placement), and each
// class's max_instances bounds its running containers (reservations).
func BundleContainers(bundle []byte) ([]container.Class, error) {
	files, err := bundleFiles(bundle, "tailworks.jsonc", "wrangler.jsonc", "wrangler.json")
	if err != nil {
		return nil, err
	}
	std := func(name string) ([]byte, error) {
		b, ok := files[name]
		if !ok {
			return nil, nil
		}
		s, err := hujson.Standardize(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return s, nil
	}
	wb, err := std("wrangler.jsonc")
	if err == nil && wb == nil {
		wb, err = std("wrangler.json")
	}
	if err != nil {
		return nil, err
	}
	tb, err := std("tailworks.jsonc")
	if err != nil {
		return nil, err
	}
	return container.Parse(wb, tb)
}

func BundleConfig(bundle []byte) (*TwConfig, error) {
	var r io.Reader = bytes.NewReader(bundle)
	if len(bundle) > 2 && bundle[0] == 0x1f && bundle[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		r = gz
	}
	tr := tar.NewReader(r)
	var best []byte
	bestDepth := 1 << 30
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag != tar.TypeReg || path.Base(name) != "tailworks.jsonc" {
			continue
		}
		if d := strings.Count(name, "/"); d < bestDepth {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			best, bestDepth = b, d
		}
	}
	if best == nil {
		return nil, errors.New("bundle has no tailworks.jsonc")
	}
	return ParseTwConfig(best)
}

// ParseTTL accepts a Go duration string ("10m") or a number of seconds.
func ParseTTL(v json.RawMessage) (time.Duration, error) {
	v = bytes.TrimSpace(v)
	if len(v) == 0 || string(v) == "null" {
		return 0, nil
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		if s == "" {
			return 0, nil
		}
		return time.ParseDuration(s)
	}
	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, fmt.Errorf("ttl: want a duration string or seconds")
	}
	return time.Duration(n * float64(time.Second)), nil
}
