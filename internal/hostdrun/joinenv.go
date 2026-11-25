package hostdrun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// Join settings (#488): `tw host add` writes the host ID, mgmt's URL, the
// control URL and (Headscale) key-tags-only into the join file next to the
// two secrets, so a new host starts with `tw-hostd -join-token-file <f>`
// and no other flags. The join keeps them beside the node state
// (<tsnet-dir>/join.env, 0600, no secrets) so later starts need no flags
// either. Precedence, per setting: flag > join file (when it exists) >
// saved join.env > default.

// Join-file keys, named after the tw-hostd flags they feed.
const (
	JoinEnvHostID      = "TW_HOST_ID"
	JoinEnvMgmtURL     = "TW_MGMT_URL"
	JoinEnvControlURL  = "TW_TSNET_CONTROL_URL"
	JoinEnvKeyTagsOnly = "TW_TSNET_KEY_TAGS_ONLY"
)

// JoinSettings are the non-secret settings a join file or join.env carries.
type JoinSettings struct {
	HostID      string
	MgmtURL     string
	ControlURL  string
	KeyTagsOnly bool
}

// joinEnvFile is where the join keeps the settings, beside the node state.
func joinEnvFile(tsDir string) string { return filepath.Join(tsDir, "join.env") }

// ReadJoinSettings reads the settings from a join file or join.env; the
// zero value when the file is missing or carries none (an older two-key
// join file). Unknown keys and the secrets are ignored.
func ReadJoinSettings(path string) JoinSettings {
	get := func(k string) string {
		v, err := tsapi.LoadEnvFile(path, k)
		if err != nil {
			return ""
		}
		return v
	}
	if path == "" {
		return JoinSettings{}
	}
	if _, err := os.Stat(path); err != nil {
		return JoinSettings{}
	}
	kt := get(JoinEnvKeyTagsOnly)
	return JoinSettings{HostID: get(JoinEnvHostID), MgmtURL: get(JoinEnvMgmtURL), ControlURL: get(JoinEnvControlURL),
		KeyTagsOnly: kt == "1" || strings.EqualFold(kt, "true")}
}

// SaveJoinSettings writes js to <tsDir>/join.env (0600, atomically). Empty
// settings are left out. Errors are returned for the caller to log: the
// node runs without the file, it just needs its flags next time.
func SaveJoinSettings(tsDir string, js JoinSettings) error {
	var b strings.Builder
	for _, kv := range [][2]string{{JoinEnvHostID, js.HostID}, {JoinEnvMgmtURL, js.MgmtURL}, {JoinEnvControlURL, js.ControlURL}} {
		if kv[1] != "" && !strings.ContainsAny(kv[1], "\n\r") {
			fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
		}
	}
	if js.KeyTagsOnly {
		b.WriteString(JoinEnvKeyTagsOnly + "=1\n")
	}
	if err := os.MkdirAll(tsDir, 0o700); err != nil {
		return err
	}
	tmp := joinEnvFile(tsDir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, joinEnvFile(tsDir))
}

// resolveJoinSettings fills o's host ID, mgmt URL, control URL and
// key-tags-only for the tsnet transport (flag > join file > join.env >
// default) and checks the required ones. It returns where each came from,
// for the "hostd running" line.
func resolveJoinSettings(o *Options, home string) (string, error) {
	if o.DataDir == "" {
		o.DataDir = filepath.Join(home, ".tailworks", "hostd")
	}
	if o.TSNetDir == "" {
		o.TSNetDir = filepath.Join(o.DataDir, "tsnet")
	}
	file := ReadJoinSettings(o.JoinTokenFile)
	saved := ReadJoinSettings(joinEnvFile(o.TSNetDir))
	var src []string
	pick := func(name string, v *string, fromFile, fromSaved string) {
		switch {
		case *v != "":
			src = append(src, name+"=flag")
		case fromFile != "":
			*v = fromFile
			src = append(src, name+"=join-file")
		case fromSaved != "":
			*v = fromSaved
			src = append(src, name+"=saved")
		}
	}
	pick("host", &o.HostID, file.HostID, saved.HostID)
	pick("mgmt", &o.MgmtAddr, file.MgmtURL, saved.MgmtURL)
	pick("control", &o.TSNetControlURL, file.ControlURL, saved.ControlURL)
	if !o.TSNetKeyTagsOnly && (file.KeyTagsOnly || saved.KeyTagsOnly) {
		o.TSNetKeyTagsOnly = true
	}
	if o.TSNetControlURL == "" {
		// A node that already joined somewhere unknown must not be pointed
		// at Tailscale's control server (an upgraded Headscale host).
		if _, err := os.Stat(filepath.Join(o.TSNetDir, "tailscaled.state")); err == nil {
			return "", errors.New("-tsnet-control-url is required (this host's node has state but no saved control URL; normal operation is tsnet only; -local-dev for the unix socket)")
		}
		o.TSNetControlURL = hostd.DefaultControlURL
		src = append(src, "control=default")
	}
	if o.MgmtAddr == "" {
		return "", errors.New("-mgmt is required: http(s)://tw-mgmt.<tailnet> (or -join-token-file with a join file from a current tw host add, which names it)")
	}
	return strings.Join(src, " "), nil
}
