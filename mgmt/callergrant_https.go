package mgmt

// CallerHTTPSPort is the port a workload node with tailworks.jsonc
// tailnet.https also serves on (its ts.net certificate, #360).
const CallerHTTPSPort = "tcp:443"

// CallerPorts are the ports the caller grant opens: CallerPort, plus
// CallerHTTPSPort for an app with tailnet.https. An app without it gets
// tcp:80 only, as before: its node listens on nothing else.
func CallerPorts(https bool) []string {
	if https {
		return []string{CallerPort, CallerHTTPSPort}
	}
	return []string{CallerPort}
}
