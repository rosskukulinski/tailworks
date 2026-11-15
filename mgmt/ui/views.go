package ui

import (
	"encoding/json"
	"fmt"
	"github.com/rosskukulinski/twd/internal/auditfmt"
	"html/template"
	"sort"
	"strings"
	"time"
)

// The view types mirror the /v1 JSON (api/openapi.yaml; mgmt/v1.go). The
// UI decodes the API's responses rather than importing mgmt's store types:
// the package cannot import mgmt (mgmt mounts it), and the JSON contract is
// the one source of truth anyway.

type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

type status struct {
	OK                   bool     `json:"ok"`
	Transport            string   `json:"transport"`
	Listeners            []string `json:"listeners"`
	PendingDeviceDeletes int      `json:"pendingDeviceDeletes"`
	Hosts                struct {
		Total int `json:"total"`
		Live  int `json:"live"`
	} `json:"hosts"`
	Tenants int    `json:"tenants"`
	Objects int    `json:"objects"`
	Version string `json:"version"`
}

type whoAmI struct {
	Kind   string   `json:"kind"`
	Login  string   `json:"login"`
	NodeID string   `json:"nodeID"`
	Tags   []string `json:"tags"`
	Admin  bool     `json:"admin"`
	Actor  string   `json:"actor"`
	Mgmt   struct {
		DNSName   string `json:"dnsName"`
		Tailnet   string `json:"tailnet"`
		HTTPS     bool   `json:"https"`
		Issuer    string `json:"issuer"`
		Transport string `json:"transport"`
		IP        string `json:"ip"`
	} `json:"mgmt"`
}

type lease struct {
	Namespace string `json:"namespace"`
	Holder    string `json:"holder"`
	Epoch     int64  `json:"epoch"`
	ExpiresAt int64  `json:"expiresAt"`
}

type host struct {
	HostID        string   `json:"hostID"`
	CapacityNodes int      `json:"capacityNodes"`
	Tiers         []string `json:"tiers"`
	NodeID        string   `json:"nodeID"`
	Pool          string   `json:"pool"`
	PushPort      int      `json:"pushPort"`
	LiveNodes     int      `json:"liveNodes"`
	LastSeen      int64    `json:"lastSeen"`
	CreatedAt     int64    `json:"createdAt"`
	Status        string   `json:"status"` // pending | live | stale
	DNSName       string   `json:"dnsName"`
	Push          *struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	} `json:"push"`
	Leases []lease `json:"leases"`
}

// PushTransport names how mgmt reaches the host's push listener.
func (h host) PushTransport() string {
	switch {
	case h.Push == nil:
		return "-"
	case h.Push.URL != "":
		return "unix"
	case h.Push.Port > 0:
		return fmt.Sprintf("tsnet:%d", h.Push.Port)
	}
	return "-"
}

type tenant struct {
	TenantID                        string         `json:"tenantID"`
	Tailnet                         string         `json:"tailnet"`
	ControlURL                      string         `json:"controlURL"`
	MintMode                        string         `json:"mintMode"`
	APIKeyFile                      string         `json:"apiKeyFile"`
	WIFClientID                     string         `json:"wifClientID"`
	WIFLifecycleClientID            string         `json:"wifLifecycleClientID"`
	OAuthClientID                   string         `json:"oauthClientID"`
	OAuthClientFile                 string         `json:"oauthClientFile"`
	RegistrationsPerMinute          int            `json:"registrationsPerMinute"`
	MaxObjects                      int            `json:"maxObjects"`
	Pool                            string         `json:"pool"`
	Tier                            string         `json:"tier"`
	CreatedAt                       int64          `json:"createdAt"`
	UpdatedAt                       int64          `json:"updatedAt"`
	MintQueue                       int            `json:"mintQueue"`
	EffectiveRegistrationsPerMinute float64        `json:"effectiveRegistrationsPerMinute"`
	OBOPublicKey                    string         `json:"oboPublicKey"`
	WIF                             map[string]any `json:"wif"`
}

type classConfig struct {
	Class string   `json:"class"`
	Tags  []string `json:"tags"`
	Agent bool     `json:"agent"`
	TTL   string   `json:"ttl"`
}

type deployment struct {
	DeploymentID string        `json:"deploymentID"`
	TenantID     string        `json:"tenantID"`
	Workload     string        `json:"workload"`
	Host         string        `json:"host"`
	Epoch        int64         `json:"epoch"`
	Status       string        `json:"status"`
	Error        string        `json:"error"`
	Namespaces   []classConfig `json:"namespaces"`
	DeployedBy   string        `json:"deployedBy"`
	CreatedAt    int64         `json:"createdAt"`
	Endpoint     struct {
		ObjectID string `json:"objectID"`
		Node     string `json:"node"`
		MagicDNS string `json:"magicDNS"`
		Status   string `json:"status"`
	} `json:"endpoint"`
}

type assignment struct {
	DeploymentID string        `json:"deploymentID"`
	TenantID     string        `json:"tenantID"`
	Workload     string        `json:"workload"`
	Namespaces   []classConfig `json:"namespaces"`
	Epoch        int64         `json:"epoch"`
	Lease        string        `json:"lease"`
	Tailnet      string        `json:"tailnet"`
	CreatedAt    int64         `json:"createdAt"`
}

type tombstone struct {
	ObjectID       string `json:"objectID"`
	TenantID       string `json:"tenantID"`
	Workload       string `json:"workload"`
	Namespace      string `json:"namespace"`
	ObjectName     string `json:"objectName"`
	KilledAt       int64  `json:"killedAt"`
	Reason         string `json:"reason"`
	By             string `json:"by"`
	Cascade        bool   `json:"cascade"`
	Root           string `json:"root"`
	DeviceDeleted  bool   `json:"deviceDeleted"`
	DeviceDeleteAt int64  `json:"deviceDeleteAt"`
}

// workloadNamespace is mgmt.WorkloadNamespace: the workload's own entry
// object (named after the workload).
const workloadNamespace = "Workload"

type object struct {
	ObjectID     string          `json:"objectID"`
	TenantID     string          `json:"tenantID"`
	Workload     string          `json:"workload"`
	Namespace    string          `json:"namespace"`
	ObjectName   string          `json:"objectName"`
	Hostname     string          `json:"hostname"`
	DeploymentID string          `json:"deploymentID"`
	Lease        string          `json:"lease"`
	Tags         []string        `json:"tags"`
	Caps         json.RawMessage `json:"caps"`
	Parent       string          `json:"parent"`
	Root         string          `json:"root"`
	Depth        int             `json:"depth"`
	TTL          int64           `json:"ttl"`
	Ephemeral    bool            `json:"ephemeral"`
	Status       string          `json:"status"`
	NodeID       string          `json:"nodeID"`
	StableNodeID string          `json:"stableNodeID"`
	MagicDNS     string          `json:"magicDNS"`
	KillReason   string          `json:"killReason"`
	KilledAt     int64           `json:"killedAt"`
	KilledBy     string          `json:"killedBy"`
	KillCascade  bool            `json:"killCascade"`
	KillRoot     string          `json:"killRoot"`
	ActingFor    string          `json:"actingFor"`
	Cert         string          `json:"cert"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
	Children     []string        `json:"children"`
	Lineage      []string        `json:"lineage"`
	Tombstone    *tombstone      `json:"tombstone"`
	// Level is the indentation in a rendered tree (set by buildTree).
	Level int `json:"-"`
}

// Live mirrors mgmt.Object.Live.
func (o object) Live() bool { return o.Status != "killed" && o.Status != "failed" }

// NodeName is the node's short name: the MagicDNS label or the hostname
// (`twd ls` NODE column).
func (o object) NodeName() string {
	if o.MagicDNS != "" {
		n, _, _ := strings.Cut(o.MagicDNS, ".")
		return n
	}
	return o.Hostname
}

// DisplayNS is `twd ls`'s CLASS/NAME column.
func (o object) DisplayNS() string {
	if o.Namespace == workloadNamespace {
		return "workload " + o.ObjectName
	}
	if o.Workload != "" {
		return o.Workload + ":" + o.Namespace + "/" + o.ObjectName
	}
	return o.Namespace + "/" + o.ObjectName
}

// CapsPretty is the caps JSON indented (empty when none).
func (o object) CapsPretty() string { return prettyJSON(o.Caps) }

type auditRecord struct {
	Seq       int64           `json:"seq"`
	Chain     string          `json:"chain"`
	TS        string          `json:"ts"`
	TenantID  string          `json:"tenantID"`
	ObjectID  string          `json:"objectID"`
	Kind      string          `json:"kind"`
	Source    string          `json:"source"`
	Dst       string          `json:"dst"`
	Code      string          `json:"code"`
	TraceID   string          `json:"traceID"`
	ActingFor string          `json:"actingFor"`
	Detail    json.RawMessage `json:"detail"`
	Hash      string          `json:"hash"`
	Raw       json.RawMessage `json:"raw"`
}

type chainStatus struct {
	Chain    string `json:"chain"`
	OK       bool   `json:"ok"`
	Records  int    `json:"records"`
	BrokenAt int64  `json:"brokenAt"`
	Error    string `json:"error"`
}

// Summary is `twd audit`'s per-chain line.
func (c chainStatus) Summary() string {
	switch {
	case !c.OK:
		return fmt.Sprintf("BROKEN at %d (%s)", c.BrokenAt, c.Error)
	case strings.HasPrefix(c.Chain, "host:"):
		return "signature ok"
	}
	return "ok"
}

type auditPage struct {
	page[auditRecord]
	Chains   []chainStatus `json:"chains"`
	Verified bool          `json:"verified"`
}

type verification struct {
	OK         bool          `json:"ok"`
	Chains     []chainStatus `json:"chains"`
	VerifiedAt int64         `json:"verifiedAt"`
}

type nodeCert struct {
	StableNodeID string         `json:"stableNodeID"`
	Status       string         `json:"status"`
	Cert         string         `json:"cert"`
	Payload      map[string]any `json:"payload"`
	OBOPublicKey string         `json:"oboPublicKey"`
}

// ---- rendering helpers ----

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ago renders a unix-ms timestamp as an age ("12s", "3m", "2h", "5d";
// "never" for 0).
func ago(ms int64, now time.Time) string {
	if ms == 0 {
		return "never"
	}
	d := now.Sub(time.UnixMilli(ms))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// stamp renders a unix-ms timestamp in UTC ("" for 0).
func stamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05Z")
}

// since renders a relative time ("5m ago") as a <time> element carrying the
// absolute instant. app.js recomputes the text in the browser, so the live
// refresh sees identical HTML (and leaves the region alone) unless the data
// itself changed. The values are formatted numbers, so the HTML is safe.
func since(ms int64, now time.Time) template.HTML {
	if ms == 0 {
		return "never"
	}
	t := time.UnixMilli(ms).UTC()
	return template.HTML(fmt.Sprintf(`<time datetime="%s" title="%s" data-ago>%s ago</time>`,
		t.Format(time.RFC3339Nano), stamp(ms), ago(ms, now)))
}

// stampStr normalises an RFC 3339 audit timestamp to the same form.
func stampStr(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05.000Z")
	}
	return s
}

// short abbreviates an identifier for dense columns (the full ID stays in
// the title attribute).
func short(s string) string {
	if len(s) <= 14 {
		return s
	}
	return s[:12] + "…"
}

// ttl renders seconds as a duration ("" for none).
func ttl(secs int64) string {
	if secs <= 0 {
		return ""
	}
	return (time.Duration(secs) * time.Second).String()
}

// badge maps a status to its CSS class: ready/live/running green,
// pending/minted/stale amber, killed/stopped/superseded grey, failed red.
func badge(st string) string {
	switch st {
	case "ready", "live", "running", "ok":
		return "ok"
	case "pending", "minted", "assigned", "stale", "deploying":
		return "warn"
	case "killed", "stopped", "superseded":
		return "off"
	case "failed", "error", "broken":
		return "bad"
	}
	return "neutral"
}

// buildTree orders objects as a lineage tree (roots by createdAt, then
// depth-first) and sets Level for indentation. Orphans (parent not in the
// set) are roots.
func buildTree(objs []object) []object {
	byID := map[string]bool{}
	kids := map[string][]object{}
	for _, o := range objs {
		byID[o.ObjectID] = true
	}
	var roots []object
	for _, o := range objs {
		if o.Parent == "" || !byID[o.Parent] {
			roots = append(roots, o)
		} else {
			kids[o.Parent] = append(kids[o.Parent], o)
		}
	}
	less := func(a, b object) bool {
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ObjectID < b.ObjectID
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	out := make([]object, 0, len(objs))
	var walk func(o object, level int)
	walk = func(o object, level int) {
		o.Level = level
		out = append(out, o)
		ks := kids[o.ObjectID]
		sort.Slice(ks, func(i, j int) bool { return less(ks[i], ks[j]) })
		for _, k := range ks {
			walk(k, level+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

// subtree returns the objects under id (inclusive) as a tree.
func subtree(objs []object, id string) []object {
	keep := map[string]bool{id: true}
	changed := true
	for changed {
		changed = false
		for _, o := range objs {
			if !keep[o.ObjectID] && keep[o.Parent] {
				keep[o.ObjectID] = true
				changed = true
			}
		}
	}
	var out []object
	for _, o := range objs {
		if keep[o.ObjectID] {
			out = append(out, o)
		}
	}
	return buildTree(out)
}

// describe renders an audit record's detail the way `twd audit` does
// (cmd/twd/ops.go describe). names maps object IDs to node names.
func describe(r auditRecord, names map[string]string) string {
	if r.Kind == "tenant_note" || r.Source == "tenant" {
		return tenantNoteText(r)
	}
	d, text := detailMap(r)
	name := func(id any) string {
		s := fmt.Sprint(id)
		if n, ok := names[s]; ok {
			return n
		}
		return s
	}
	if d == nil {
		return text
	}
	return auditfmt.Describe(r.Kind, r.ObjectID, r.ActingFor, r.Code, d, name)
}

func detailMap(r auditRecord) (map[string]any, string) {
	var m map[string]any
	if len(r.Detail) > 0 && json.Unmarshal(r.Detail, &m) == nil {
		return m, ""
	}
	var raw struct {
		Detail any    `json:"detail"`
		Dst    string `json:"dst"`
		Code   string `json:"code"`
	}
	if len(r.Raw) > 0 && json.Unmarshal(r.Raw, &raw) == nil {
		s := fmt.Sprint(raw.Detail)
		if raw.Detail == nil {
			s = ""
		}
		if raw.Dst != "" {
			s = "→ " + raw.Dst + " " + s
		}
		if raw.Code != "" {
			s += " " + raw.Code
		}
		return nil, strings.TrimSpace(s)
	}
	var str string
	if len(r.Detail) > 0 && json.Unmarshal(r.Detail, &str) == nil {
		return nil, str
	}
	if len(r.Detail) > 0 {
		return nil, string(r.Detail)
	}
	return nil, ""
}

// tenantNoteText: unverified content written by tenant code, never a
// platform decision, whatever it claims (RT-02).
func tenantNoteText(r auditRecord) string {
	_, text := detailMap(r)
	var raw struct {
		Detail string `json:"detail"`
	}
	if len(r.Raw) > 0 && json.Unmarshal(r.Raw, &raw) == nil && raw.Detail != "" {
		text = raw.Detail
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return "tenant note (unverified, written by tenant code): " + text
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
