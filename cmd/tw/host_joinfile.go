package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The join file `tw host add` writes carries, besides the two secrets,
// everything tw-hostd needs to start, so a new host runs
// `tw-hostd -join-token-file <file>` with no other flags (#488). The keys
// are named after the tw-hostd flags they feed (not the tenant
// -control-url override). tw-hostd ignores keys it doesn't know, and a
// flag still wins over the file.

// hostJoinFileContent is the join file for hostID: TS_AUTHKEY and
// TW_REGISTRATION_SECRET (both secret), then TW_HOST_ID, TW_MGMT_URL
// (when known), TW_TSNET_CONTROL_URL and TW_TSNET_KEY_TAGS_ONLY
// (Headscale). Written 0600; never logged.
func (hj hostJoin) hostJoinFileContent(hostID, mgmtURL string) []byte {
	var b strings.Builder
	b.Write(hj.joinFileContent())
	fmt.Fprintf(&b, "TW_HOST_ID=%s\n", hostID)
	if mgmtURL != "" {
		fmt.Fprintf(&b, "TW_MGMT_URL=%s\n", mgmtURL)
	}
	if hj.ControlURL != "" {
		fmt.Fprintf(&b, "TW_TSNET_CONTROL_URL=%s\n", hj.ControlURL)
	}
	if hj.KeyTagsOnly {
		b.WriteString("TW_TSNET_KEY_TAGS_ONLY=1\n")
	}
	return []byte(b.String())
}

// hostJoinMgmtURL is mgmt's tailnet URL for a new host: mgmt's own answer,
// else (an older mgmt) the linked node name this CLI knows.
func (s *state) hostJoinMgmtURL(hj hostJoin) string {
	if hj.MgmtURL != "" {
		return hj.MgmtURL
	}
	if s.Cfg.MgmtDNSName != "" {
		return "http://" + s.Cfg.MgmtDNSName
	}
	return ""
}

// printHostRunbook is what `tw host add` prints for the new machine.
func printHostRunbook(hostID, path string) {
	base := filepath.Base(path)
	fmt.Printf("  on %s:  install tw, copy the file there (scp, 0600), then\n", hostID)
	fmt.Printf("    tw-hostd -join-token-file %s\n", base)
	fmt.Printf("  the file names the host ID, mgmt and the control server, so no other flags are needed; the key is used once by the\n")
	fmt.Printf("  join, the file is deleted and its settings are kept beside the node state (registration secret included);\n")
	fmt.Printf("  mgmt binds the node to host %s on its first PUT /v1/hosts/%s, which must present that secret\n", hostID, hostID)
	fmt.Printf("  as a service: systemd with Restart=always, and tw-hostd -join-token-file %s -self-upgrade (docs/INSTALL.md)\n", base)
}
