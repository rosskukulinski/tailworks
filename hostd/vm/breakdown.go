package vm

import (
	"math"
	"sort"

	"github.com/rosskukulinski/tailworks/hostd/sandbox"
)

// Stage is one step of a microVM start, in ms.
type Stage struct {
	Name string  `json:"name"`
	MS   float64 `json:"ms"`
}

// Breakdown turns a booted VM's stage timestamps (sandbox.Info.Stages,
// host clock since Boot() was called; GuestStages, guest kernel clock) into
// per-stage durations, in boot order. The guest's clock starts at
// InstanceStart (Firecracker starts the vCPUs right then), so guest stages
// are placed after the host's "instance_start".
//
// Stages a path doesn't have (no volume; a pooled or restored VM has no
// kernel boot) are omitted.
func Breakdown(i sandbox.Info) []Stage {
	h, g := i.Stages, i.GuestStages
	var out []Stage
	add := func(name string, from, to float64, ok bool) {
		if ok && to >= from {
			out = append(out, Stage{name, round(to - from)})
		}
	}
	hv := func(k string) (float64, bool) { v, ok := h[k]; return v, ok }
	gv := func(k string) (float64, bool) { v, ok := g[k]; return v, ok }
	chain := func(prefix string, src func(string) (float64, bool), start float64, keys ...[2]string) {
		prev := start
		for _, k := range keys {
			if v, ok := src(k[0]); ok {
				add(prefix+k[1], prev, v, true)
				prev = v
			}
		}
	}
	if _, ok := h["claim"]; ok {
		if i.Source == "restore" {
			// The restore itself (on demand in Boot(), or earlier at pool
			// fill), relative to its own start.
			chain("restore.", hv, 0,
				[2]string{"pool.config_drive", "config drive"},
				[2]string{"pool.helper.netns", "netns + bridge + 2 TAPs"},
				[2]string{"pool.helper.jail", "jail dir"},
				[2]string{"pool.helper.restore_jail", "snapshot state copy + uffd socket + base memory mmap"},
				[2]string{"pool.helper.jailer", "jailer → firecracker API socket up"},
				[2]string{"pool.helper.snapshot_load", "PUT /snapshot/load (uffd handshake, resume)"},
				[2]string{"pool.vnet", "helper reply + host stack"},
				[2]string{"pool.pooled", "guest resumes, vsock reconnect, says pooled"},
			)
		}
		// Warm pool / snapshot restore: the VM was booted (or restored)
		// before; Boot() is claim + inject + workerd start.
		chain("host.", hv, 0,
			[2]string{"restore", "snapshot restore, on demand (all of the above)"},
			[2]string{"claim", "claim (take a pooled VM)"},
			[2]string{"volume_attach", "volume attach (helper: loop + LUKS2 open + PATCH drive)"},
			[2]string{"inject", "inject bundle + config over vsock (incl. guest extract, reseed, mount)"},
		)
	} else {
		chain("host.", hv, 0,
			[2]string{"config_drive", "config drive (tar of the instance dir)"},
			[2]string{"helper_dial", "helper connect"},
			[2]string{"helper.netns", "netns + bridge + 2 TAPs"},
			[2]string{"helper.jail", "jail dir (bind mounts, config copy, vsock listener)"},
			[2]string{"helper.volume_done", "volume attach (loop + LUKS2 open)"},
			[2]string{"helper.jailer", "jailer → firecracker API socket up"},
			[2]string{"helper.api/vsock", "VMM API calls (machine, boot, drives, net, vsock)"},
			[2]string{"helper.instance_start", "InstanceStart call"},
		)
	}
	workerd := [][2]string{
		{"workerd_spawn", "workerd process start (fork/exec)"},
		{"workerd_listen", "workerd/V8 init (until ingress listens)"},
		{"healthy", "isolate + bundle load + first healthy response"},
	}
	if _, warm := h["claim"]; warm {
		// The guest was idle until the host's inject message arrived (the
		// guest stages overlap the host's "inject" above).
		chain("guest.", gv, g["inject_rx"], append([][2]string{
			{"inject_done", "bundle extract + reseed + clock"},
			{"volume", "DO volume mount"},
		}, workerd...)...)
	} else {
		add("guest.kernel boot (InstanceStart → init)", 0, g["init"], g["init"] > 0)
		chain("guest.", gv, g["init"], append([][2]string{
			{"mounts", "init: mount pseudo filesystems"},
			{"config", "config drive read + extract"},
			{"clock", "set clock"},
			{"net", "eth0 up (rtnetlink)"},
			{"volume", "DO volume mount"},
		}, workerd...)...)
	}
	if r, ok := hv("ready"); ok {
		out = append(out, Stage{"total Boot()", round(r)})
	}
	return out
}

func round(v float64) float64 { return math.Round(v*100) / 100 }

// Percentiles summarizes samples (ms) as p50 / p95 / min / max / n.
func Percentiles(xs []float64) map[string]float64 {
	if len(xs) == 0 {
		return nil
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	q := func(p float64) float64 {
		// nearest-rank
		i := int(math.Ceil(p*float64(len(c)))) - 1
		if i < 0 {
			i = 0
		}
		return round(c[i])
	}
	return map[string]float64{"p50": q(0.5), "p95": q(0.95), "min": round(c[0]), "max": round(c[len(c)-1]), "n": float64(len(c))}
}

// Summarize collects Breakdown()s of many boots into per-stage percentiles,
// keeping the stage order of the first.
func Summarize(infos []sandbox.Info) (order []string, stats map[string]map[string]float64) {
	samples := map[string][]float64{}
	for _, i := range infos {
		for _, s := range Breakdown(i) {
			if _, ok := samples[s.Name]; !ok {
				order = append(order, s.Name)
			}
			samples[s.Name] = append(samples[s.Name], s.MS)
		}
	}
	stats = map[string]map[string]float64{}
	for k, v := range samples {
		stats[k] = Percentiles(v)
	}
	return order, stats
}
