package netd

import (
	"strconv"

	"github.com/rosskukulinski/tailworks/netd/publicip"
)

// CodeInternetPortRefused (403) is the default port policy on the
// internet path (ADR-052 follow-up, 2026-11-24; issue #339): TCP port 25
// to a public address is refused unless the workload's
// tailnet.egress.internet names the destination with that port.
const CodeInternetPortRefused = "internet_port_refused"

// internetPortPolicy decides whether an internet destination the
// workload's setting doesn't refuse outright may go to port. pol is the
// object's InternetPolicy (Off already handled). It returns a code and
// detail, or "" to allow:
//
//   - internet_egress_unavailable when pol.Allow is a list and no entry
//     covers host:port (an entry that names a port covers that port only);
//   - internet_port_refused when publicip.RefusedPort refuses the port
//     and no entry names it for this host.
//
// Every internet path that names a port (fetch(), connect(), node:net, the
// Node tier's CONNECT) reaches this through internetRoute.
func internetPortPolicy(host string, port uint16, pol InternetPolicy) (code, detail string) {
	var list publicip.Allowlist
	if pol.Allow != nil {
		list = publicip.ParseAllowlist(pol.Allow)
		if !list.AllowsPort(host, port) {
			if list.Allows(host) {
				return CodeInternetEgressUnavailable, internetEgressDetail(host, "its tailnet.egress.internet entry names another port, not "+strconv.Itoa(int(port)))
			}
			return CodeInternetEgressUnavailable, internetEgressDetail(host, "it is not on this workload's tailnet.egress.internet allowlist")
		}
	}
	if why := publicip.RefusedPort(port); why != "" && !(pol.Allow != nil && list.NamesPort(host, port)) {
		return CodeInternetPortRefused, why + ". To allow it for this destination, list it with the port in tailnet.egress.internet (" +
			`"` + host + ":" + strconv.Itoa(int(port)) + `"` + "), or use a gateway on your tailnet: " + RecipeInternetAPI
	}
	return "", ""
}
