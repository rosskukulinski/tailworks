package main

import "testing"

// TestConsoleURL: the console line follows mountUI's gate and is left out
// where this machine's browser can't reach mgmt.
func TestConsoleURL(t *testing.T) {
	ep := &Link{Endpoint: "https://tw-mgmt.example.ts.net/"}
	tsnet := &Link{Endpoint: "https://tw-mgmt.example.ts.net", Tsnet: &LinkTsnet{}}
	for _, c := range []struct {
		name                         string
		l                            *Link
		admin, tenantAdmin, approver bool
		want                         string
	}{
		{"admin", ep, true, false, false, "https://tw-mgmt.example.ts.net/ui/"},
		{"tenant admin", ep, false, true, false, "https://tw-mgmt.example.ts.net/ui/"},
		{"approver", ep, false, false, true, "https://tw-mgmt.example.ts.net/ui/approvals"},
		{"nobody", ep, false, false, false, ""},
		{"tsnet", tsnet, true, false, false, ""},
		{"unlinked", nil, true, false, false, ""},
	} {
		if got := consoleURL(c.l, c.admin, c.tenantAdmin, c.approver); got != c.want {
			t.Errorf("%s: consoleURL = %q, want %q", c.name, got, c.want)
		}
	}
}
