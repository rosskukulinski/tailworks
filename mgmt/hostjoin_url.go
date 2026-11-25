package mgmt

import "strings"

// hostJoinMgmtURL is where a new host reaches mgmt over the tailnet
// (`tw-hostd -mgmt`), returned with its join key so `tw host add` can put it
// in the join file from any machine (#488). "" when mgmt doesn't know its
// own node name (local-dev).
func (a *v1api) hostJoinMgmtURL() string {
	dns := strings.TrimSuffix(a.s.Self.DNSName, ".")
	if dns == "" {
		return ""
	}
	return "http://" + dns
}
