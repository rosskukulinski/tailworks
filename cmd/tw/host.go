package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// cmdHost: `tw host setup --microvm` (one time, with sudo, Linux) installs
// the microVM tier's privileged helper; `tw host setup --undo` removes it.
func cmdHost(args []string) error {
	// The subcommand is the first positional (global flags such as
	// --state-dir X may come first).
	sub := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--state-dir" || a == "-state-dir" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			sub = i
			break
		}
	}
	if sub < 0 {
		return needsSubcommand("host")
	}
	rest := append(append([]string{}, args[:sub]...), args[sub+1:]...)
	switch normalizeVerb(args[sub]) {
	case "setup":
		return cmdHostSetup(rest)
	case "vmcheck":
		return cmdHostVMCheck(rest)
	case "add":
		return cmdHostAdd(rest)
	case "ls":
		return cmdHostLs(rest)
	case "rm":
		return cmdHostRm(rest)
	case "cordon":
		return cmdHostCordon(rest, true) // host_cordon.go
	case "uncordon":
		return cmdHostCordon(rest, false)
	case "drain":
		return cmdHostDrain(rest)
	}
	return fail("bad_request", fmt.Sprintf("unknown host subcommand %q", args[sub]), "usage: tw "+usageOf("host"))
}

// setupResult is `tw host setup --json`.
type setupResult struct {
	Action    string   `json:"action"` // setup | undo
	User      string   `json:"user,omitempty"`
	UID       int      `json:"uid,omitempty"`
	Steps     []string `json:"steps"`
	Socket    string   `json:"socket,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	HugePages int      `json:"hugePages,omitempty"`
}

func cmdHostSetup(args []string) error {
	f := newFlags("host")
	microvm := f.Bool("microvm", false, "install the microVM tier's privileged helper (tw-vmhelper) for --user")
	undo := f.Bool("undo", false, "remove everything `tw host setup --microvm` installed")
	userF := f.String("user", "", "the user tailworks runs as (default: $SUDO_USER)")
	huge := f.Int("hugepages", -1, "2 MiB huge pages to reserve for guest RAM (default: enough for 4 VMs, at most a quarter of RAM; 0 = none)")
	fc := f.String("firecracker", "", "firecracker binary to install (default: the release's share/tailworks/vm/firecracker)")
	jl := f.String("jailer", "", "jailer binary to install (default: the release's share/tailworks/vm/jailer)")
	f.hidden("to install other firecracker builds", "firecracker", "jailer") // helpflags.go
	if _, err := f.parse(args); err != nil {
		return err
	}
	if *microvm == *undo {
		return fail("bad_request", "pass exactly one of --microvm or --undo", "usage: tw "+usageOf("host"))
	}
	var res *setupResult
	var err error
	if *undo {
		res, err = hostUndo()
	} else {
		res, err = hostSetupMicroVM(*userF, *huge, *fc, *jl)
	}
	if err != nil {
		return err
	}
	emit(f, res, func() {
		for _, s := range res.Steps {
			fmt.Println(s)
		}
		if res.Action == "setup" {
			fmt.Printf("\nmicroVM tier ready for %s. As %s (no sudo): tw up …, then deploy with \"tier\": \"microvm\" in tailworks.jsonc.\n", res.User, res.User)
			fmt.Println("Check it with `tw doctor`. Remove it with `sudo tw host setup --undo`.")
		}
	})
	return nil
}

// cmdHostVMCheck: `tw host vmcheck <workload> [--keyscan]` checks a
// running microVM workload: guest memory, DO volume fsync latency, and
// with --keyscan that no tailnet or volume key is in the guest (its disks,
// files, and a full snapshot of its RAM). With --container <Class>/<name>
// it scans that object's container guest instead, with the stricter
// sandbox needle set (ADR-032 D5). Output has no key material.
func cmdHostVMCheck(args []string) error {
	f := newFlags("host")
	keyscan := f.Bool("keyscan", false, "also scan the guest's disks, files and RAM for key material")
	ctr := f.String("container", "", "scan this object's container guest (<Class>/<name>; implies --keyscan)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "usage: tw host vmcheck <workload> [--keyscan] [--container <Class>/<name>]")
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	q := url.Values{"workload": {pos[0]}}
	if *keyscan || *ctr != "" {
		q.Set("keyscan", "1")
	}
	if *ctr != "" {
		q.Set("container", *ctr)
		var rep hostd.ContainerCheckReport
		if err := s.hostd().Do(ctx, "POST", "/v1/vm/check?"+q.Encode(), nil, &rep); err != nil {
			return err
		}
		k := rep.KeyScan
		bad := k == nil || len(k.Found) > 0 || len(k.GuestStateF) > 0 || !strings.HasPrefix(k.Control, "guest canary found")
		emit(f, rep, func() {
			mark := "✓"
			if bad {
				mark = "✗"
			}
			if k != nil {
				fmt.Printf("%s no keys in container %s/%s/%s (VMM pid %d): %d needles (%s) · found %v · %d guest files · %d MiB RAM · %s\n",
					mark, rep.Workload, rep.Class, rep.Object, rep.PID, k.Needles, strings.Join(k.NeedleKinds, ", "), k.Found, k.GuestFiles, k.MemoryMiB, k.Control)
			}
		})
		if bad {
			return fail("keys_in_guest", fmt.Sprintf("container keyscan failed: %+v", k))
		}
		return nil
	}
	var rep hostd.VMCheckReport
	if err := s.hostd().Do(ctx, "POST", "/v1/vm/check?"+q.Encode(), nil, &rep); err != nil {
		return err
	}
	emit(f, rep, func() {
		fmt.Printf("✓ microVM %s · VMM pid %d · RSS %d MiB\n", rep.Workload, rep.PID, rep.RSSKB/1024)
		if st := rep.Stats; st != nil {
			fmt.Printf("  guest: %d/%d MiB used, workerd RSS %d MiB · DO storage %s (%d KiB used)\n",
				(st.MemTotalKB-st.MemAvailableKB)/1024, st.MemTotalKB/1024, st.WorkerdRSSKB/1024, st.DOFS, st.DOUsedKB)
		}
		if b := rep.FSBench; b != nil {
			fmt.Printf("  DO volume 4 KiB write+fdatasync: write p50 %d µs, fsync p50 %d µs / p95 %d µs (n=%d)\n", b.WriteP50US, b.SyncP50US, b.SyncP95US, b.N)
		}
		if k := rep.KeyScan; k != nil {
			mark := "✓"
			if len(k.Found) > 0 || len(k.GuestStateF) > 0 {
				mark = "✗"
			}
			fmt.Printf("%s no keys in guest: %d needles (%s) · found %v · %d guest files · %d MiB RAM · %s\n",
				mark, k.Needles, strings.Join(k.NeedleKinds, ", "), k.Found, k.GuestFiles, k.MemoryMiB, k.Control)
		}
	})
	if rep.KeyScan != nil && (len(rep.KeyScan.Found) > 0 || len(rep.KeyScan.GuestStateF) > 0) {
		return fail("keys_in_guest", fmt.Sprintf("key material in the guest: %v %v", rep.KeyScan.Found, rep.KeyScan.GuestStateF))
	}
	return nil
}

// cmdHostAdd is `tw host add <host-id> [--tenant …] [--out <file>]`:
// mgmt records the host (status pending) and mints a single-use,
// 10-minute tag:tw-host join key through the tenant's credential (its
// OAuth client, or the Headscale API key). The key goes to --out as a TS_AUTHKEY=… file (0600; default
// <host-id>.join.env in the current directory), never to the terminal or
// the log, with the host ID, mgmt's URL and the control server. On the new
// machine: `tw-hostd -join-token-file <file>`.
func cmdHostAdd(args []string) error {
	f := newFlags("host add")
	tenantF := tenantFlag(f)
	out := f.String("out", "", "write the join file here (default ./<host-id>.join.env): the join key (TS_AUTHKEY=…), the one-time registration secret (TW_REGISTRATION_SECRET=…), and the host ID, mgmt URL and control URL tw-hostd -join-token-file reads")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw host add needs one host ID", "usage: tw "+usageOf("host"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = pos[0] + ".join.env"
	}
	path, _ = filepath.Abs(path)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var hj hostJoin
	if err := s.mgmt().DoHeaders(ctx, "POST", "/v1/hosts", map[string]any{"hostID": pos[0], "tenantID": tn.TenantID}, &hj,
		map[string]string{"Tw-Actor": actor()}); err != nil {
		if me, ok := mgmtapi.AsError(err); ok && me.Code == "exists" {
			return fail("exists", me.Message).withFix("tw host rm " + pos[0] + " --force, then tw host add again")
		}
		return err
	}
	if hj.RegistrationSecret == "" {
		return fail("bootstrap_failed", "mgmt returned no registration secret for host "+pos[0]).withFix("upgrade tw-mgmt (POST /v1/hosts must answer registrationSecret)")
	}
	mgmtURL := s.hostJoinMgmtURL(hj) // host_joinfile.go
	if err := os.WriteFile(path, hj.hostJoinFileContent(pos[0], mgmtURL), 0o600); err != nil {
		return err
	}
	res := map[string]any{"hostID": pos[0], "hostname": hj.Hostname, "tags": hj.Tags, "expiresAt": hj.ExpiresAt, "controlURL": hj.ControlURL,
		"tailnet": hj.Tailnet, "joinFile": path, "mgmt": mgmtURL}
	emit(f, res, func() {
		fmt.Printf("✓ host     %s added (pending) · join key for %s (%s) written to %s (single use, expires %s)\n", pos[0], hj.Hostname,
			strings.Join(hj.Tags, ","), path, time.UnixMilli(hj.ExpiresAt).Local().Format("15:04:05"))
		printHostRunbook(pos[0], path) // host_joinfile.go
	})
	return nil
}

// cmdHostLs is `tw host ls` (GET /v1/hosts, every page).
func cmdHostLs(args []string) error {
	f := newFlags("host ls")
	if _, err := f.parse(args); err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type hostRow struct {
		HostID    string   `json:"hostID"`
		Status    string   `json:"status"`
		Tiers     []string `json:"tiers"` // what it offers (HostTier): its isolation tiers and containers
		Pool      string   `json:"pool"`
		NodeID    string   `json:"nodeID"`
		DNSName   string   `json:"dnsName"`
		LiveNodes int      `json:"liveNodes"`
		LastSeen  int64    `json:"lastSeen"`
		Leases    []struct {
			Namespace string `json:"namespace"`
			Epoch     int64  `json:"epoch"`
		} `json:"leases"`

		// The host's GOARCH (container images must match it) and features.
		Arch     string   `json:"arch,omitempty"`
		Features []string `json:"features"`
	}
	hosts, err := listAll[hostRow](ctx, s.mgmt(), "/v1/hosts", 0)
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].Features == nil {
			hosts[i].Features = []string{} // empty lists are [], never null (docs/cli-json.md)
		}
	}
	emit(f, hosts, func() {
		fmt.Printf("  %-16s %-8s %-26s %-6s %-36s %-6s %-10s %s\n", "HOST", "STATUS", "OFFERS", "ARCH", "NODE", "LIVE", "SEEN", "LEASES")
		for _, h := range hosts {
			seen := "never"
			if h.LastSeen > 0 {
				seen = time.Since(time.UnixMilli(h.LastSeen)).Round(time.Second).String() + " ago"
			}
			var ls []string
			for _, l := range h.Leases {
				ls = append(ls, fmt.Sprintf("%s@%d", l.Namespace, l.Epoch))
			}
			arch := h.Arch
			if arch == "" {
				arch = "-"
			}
			fmt.Printf("  %-16s %-8s %-26s %-6s %-36s %-6d %-10s %s\n", tsafe(h.HostID), tsafe(h.Status), tsafe(strings.Join(h.Tiers, ",")), tsafe(arch), tsafe(h.DNSName), h.LiveNodes, seen, tsafe(strings.Join(ls, " ")))
		}
	})
	return nil
}

// cmdHostRm is `tw host rm <host-id> [--force]` (DELETE /v1/hosts/{id}).
// --force sends the API's ?drain=true: it kills the host's objects and
// releases its leases, losing whatever they kept on that host. It is not
// `tw host drain`, which moves workloads off without losing data. The flag
// used to be --drain; that spelling is still accepted, as a deprecated alias.
func cmdHostRm(args []string) error {
	f := newFlags("host rm")
	force := f.Bool("force", false, "kill the host's objects and release its leases first (their data on that host is lost; to move them instead, tw host drain)")
	drain := f.Bool("drain", false, "deprecated: same as --force")
	yes := yesFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw host rm needs one host ID", "usage: tw "+usageOf("host"))
	}
	if err := confirmDestroy(f, *yes, "removing host "+pos[0]+" deregisters it (it must be added again to serve)", pos[0], "tw host rm "+pos[0]); err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	q := ""
	if *force || *drain {
		q = "?drain=true"
	}
	var res struct {
		HostID         string   `json:"hostID"`
		Killed         []string `json:"killed"`
		LeasesReleased []string `json:"leasesReleased"`
		DevicesLeft    []string `json:"devicesLeft"`
	}
	if err := s.mgmt().DoHeaders(ctx, "DELETE", "/v1/hosts/"+pos[0]+q, nil, &res, map[string]string{"Tw-Actor": actor()}); err != nil {
		if me, ok := mgmtapi.AsError(err); ok && me.Code == "host_holds_leases" {
			return fail("host_holds_leases", me.Message).withFix("tw host drain "+pos[0]+"   (move its workloads off first)", "or tw host rm "+pos[0]+" --force --yes   (kill them; their data on that host is lost)")
		}
		return err
	}
	res.HostID = pos[0]
	emit(f, res, func() {
		fmt.Printf("✓ host     %s removed · %d objects killed · %d leases released\n", pos[0], len(res.Killed), len(res.LeasesReleased))
		if len(res.DevicesLeft) > 0 {
			fmt.Printf("› devices  delete the host's own node in the admin console: %s\n", strings.Join(res.DevicesLeft, ", "))
		}
	})
	return nil
}
