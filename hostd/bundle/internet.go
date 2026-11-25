package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/netd/publicip"
)

// ViaExitNode is tailworks.jsonc tailnet.egress.via "exit-node"
// (ADR-054): the workload's internet egress must leave
// through a tailnet exit node, so it is placed only on hosts whose
// internet path is one (features.InternetExitNode). Absent: whatever the
// host offers. It says how; tailnet.egress.internet says whether and
// where to.
const ViaExitNode = "exit-node"

// InternetEgress is tailworks.jsonc tailnet.egress.internet
// (ADR-049): one setting for Workers and containers.
//
//	absent  Workers may reach any public host; containers reach none
//	true    Workers and containers (with enableInternet) may reach any public host
//	false   neither may reach the internet (also: [])
//	[...]   both are limited to these hosts ("*.suffix", IPs, CIDRs)
//
// The SSRF guard (netd/publicip) applies to every case.
type InternetEgress struct {
	// Set is false when the key is absent.
	Set bool
	// All is true for `true`; Hosts is the list form (non-nil, maybe
	// empty). With Set and neither, the value was false.
	All   bool
	Hosts []string
}

// UnmarshalJSON accepts true, false, null (absent) or an array of strings.
func (e *InternetEgress) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch string(b) {
	case "null":
		*e = InternetEgress{}
		return nil
	case "true":
		*e = InternetEgress{Set: true, All: true}
		return nil
	case "false":
		*e = InternetEgress{Set: true}
		return nil
	}
	var hosts []string
	if err := json.Unmarshal(b, &hosts); err != nil {
		return errors.New("tailnet.egress.internet: want true, false or a list of hosts")
	}
	if hosts == nil {
		hosts = []string{}
	}
	for _, h := range hosts {
		if !publicip.EntryPortOK(h) {
			return fmt.Errorf("tailnet.egress.internet: %q: a host may be followed by \":<port>\" (1-65535), as in \"smtp.example.com:25\"", h)
		}
	}
	*e = InternetEgress{Set: true, Hosts: hosts}
	return nil
}

// NamesPort reports whether some list entry names a port
// ("smtp.example.com:25"): the port policy's override (ADR-052 follow-up),
// which hosts older than it read as a name that matches nothing
// (features.InternetPorts).
func (e InternetEgress) NamesPort() bool {
	for _, h := range e.Hosts {
		if _, port := publicip.SplitEntryPort(strings.ToLower(strings.TrimSpace(h))); port != 0 {
			return true
		}
	}
	return false
}

// Boolean reports whether the value was true or false (not a list):
// hosts older than Worker internet egress can't parse that form.
func (e InternetEgress) Boolean() bool {
	return e.Set && e.Hosts == nil
}

// WorkerOff reports whether Worker (and Node app) internet egress is off.
func (e InternetEgress) WorkerOff() bool {
	return e.Set && !e.All && len(e.Hosts) == 0
}

// WorkerAllow is the Workers' allowlist: nil for any public host (absent
// or true), else the list.
func (e InternetEgress) WorkerAllow() []string {
	if !e.Set || e.All {
		return nil
	}
	return e.Hosts
}

// Containers is the allowlist a container started with enableInternet
// is limited to: absent or false allow nothing (as before Workers had
// internet egress), true allows any public host.
func (e InternetEgress) Containers() []string {
	switch {
	case !e.Set:
		return nil
	case e.All:
		return []string{"*", "0.0.0.0/0", "::/0"}
	}
	return e.Hosts
}
