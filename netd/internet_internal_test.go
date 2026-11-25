package netd

import (
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestTailnetShaped(t *testing.T) {
	st := &ipnstate.Status{
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "corp.example"},
		Self:           &ipnstate.PeerStatus{DNSName: "tw-a.corp.example."},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {DNSName: "shared-db.other-tailnet.ts.net."},
			key.NewNode().Public(): {DNSName: "box.headscale.lan."},
		},
	}
	for name, want := range map[string]bool{
		"db":                       true, // a bare MagicDNS short name
		"localhost":                true, // bare: stays peer_not_visible
		"db.corp.example":          true, // this tailnet's suffix
		"x.tailabc.ts.net":         true, // any *.ts.net name
		"old.beta.tailscale.net":   true,
		"other.headscale.lan":      true, // a visible peer's suffix
		"api.github.com":           false,
		"example.com":              false,
		"metadata.google.internal": false,
		"corp.example":             false, // the suffix itself is not a node name
		"evilts.net":               false,
	} {
		if got := tailnetShaped(name, st); got != want {
			t.Errorf("tailnetShaped(%q) = %v, want %v", name, got, want)
		}
	}
	if !tailnetShaped("db", nil) || tailnetShaped("example.com", nil) || !tailnetShaped("a.b.ts.net", nil) {
		t.Error("nil status")
	}
}
