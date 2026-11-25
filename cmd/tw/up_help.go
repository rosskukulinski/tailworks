package main

// tw up --help's flag groups (helpflags.go, issue #463). The everyday
// flags (--oauth-client-file, --apply-policy, --yes, --service) are in no
// group, so they come first. up_store.go, up_retention.go and
// up_workerinternet.go add their own flags to the groups declared here.

const (
	upGroupControl   = "Control plane (Tailscale or Headscale)"
	upGroupStore     = "Store and custody (Postgres, setec)"
	upGroupRetention = "Retention"
	upGroupLayout    = "Layout and identity"
	upGroupIsolation = "Isolation and egress"
	upGroupTuning    = "Tuning"
	upGroupDev       = "Development and testing"
)

// upFlagGroups declares tw up's groups, in --help order.
func upFlagGroups(f *flags) {
	f.group(upGroupControl,
		"control",
		"control-url",
		"api-key-file",
		"tailnet",
	)
	f.group(upGroupStore)     // up_store.go
	f.group(upGroupRetention) // up_retention.go
	f.group(upGroupLayout,
		"host-id",
		"hostname-prefix",
		"mgmt-hostname",
		"separate-host",
		"ephemeral",
	)
	f.group(upGroupIsolation, // and up_workerinternet.go
		"insecure-shared-host",
		"allow-experimental-flags",
	)
	f.group(upGroupTuning,
		"warm-keys",
		"pool-nodes",
		"port-mapping",
	)
	f.group(upGroupDev,
		"workerd",
		"mgmt-authkey-file",
	)
	f.hidden("for the test harness",
		"allow-sibling-egress",
		"local-dev",
		"tsapi-url",
	)
}
