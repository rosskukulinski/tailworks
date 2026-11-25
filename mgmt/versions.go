package mgmt

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/features"
	"github.com/rosskukulinski/tailworks/internal/version"
)

// Versions, skew and features (ADR-041 §7).

// Version is this replica's tw-mgmt version: Config.Version, else the
// built-in one.
func (m *Mgmt) Version() string {
	if m.cfg.Version != "" {
		return m.cfg.Version
	}
	return version.Version
}

// hostSkew is why h may not take new work from this mgmt ("" = within the
// window). A host out of the window keeps heartbeating (its running
// workloads and leases are not disturbed) but placement skips it.
func (m *Mgmt) hostSkew(h Host) string {
	if err := version.CheckHost(h.Version, m.Version()); err != nil {
		return err.Error()
	}
	return ""
}

// hostVersionName is the host's version for messages.
func hostVersionName(h Host) string {
	if h.Version == "" {
		return "tw " + version.Legacy + " or older (no version reported)"
	}
	return "tw " + h.Version
}

// featureGap is why h can't run a deployment needing need ("" = it can).
func featureGap(h Host, need []string) string {
	miss := features.Missing(need, h.Features)
	if len(miss) == 0 {
		return ""
	}
	return fmt.Sprintf("lacks feature %s (%s; upgrade the host)", strings.Join(miss, ","), hostVersionName(h))
}

// VersionStatus is the "versions" object of GET /v1/status.
type VersionStatus struct {
	// Mgmt is every live replica's version, by replica ID.
	Mgmt map[string]string `json:"mgmt"`
	// Hosts counts hosts by reported version ("" = none reported).
	Hosts map[string]int `json:"hosts"`
	// Skewed lists hosts outside the skew window (placement skips them).
	Skewed []string `json:"skewed,omitempty"`
	// Schema is the store's migration status.
	Schema *SchemaStatus `json:"schema,omitempty"`
}

func (m *Mgmt) versionStatus(ctx context.Context, hosts []Host) VersionStatus {
	vs := VersionStatus{Mgmt: map[string]string{m.replicaID: m.Version()}, Hosts: map[string]int{}}
	now := m.now()
	if rs, err := m.store.ListReplicas(ctx); err == nil {
		for _, r := range rs {
			if r.ID == m.replicaID || r.LastSeen == 0 || now.Sub(time.UnixMilli(r.LastSeen)) > ReplicaLiveTTL {
				continue
			}
			vs.Mgmt[r.ID] = r.Version
		}
	}
	for _, h := range hosts {
		vs.Hosts[h.Version]++
		if m.hostSkew(h) != "" {
			vs.Skewed = append(vs.Skewed, h.ID)
		}
	}
	sort.Strings(vs.Skewed)
	if ss, ok := m.store.(interface {
		SchemaStatus(context.Context) (SchemaStatus, error)
	}); ok {
		if st, err := ss.SchemaStatus(ctx); err == nil {
			vs.Schema = &st
		}
	}
	return vs
}

// replicaSkew is why the live replicas can't run next to this one ("" =
// they can): replicas more than one release line apart.
func (m *Mgmt) replicaSkew(rs []Replica) string {
	now := m.now()
	for _, r := range rs {
		if r.ID == m.replicaID || r.LastSeen == 0 || now.Sub(time.UnixMilli(r.LastSeen)) > ReplicaLiveTTL {
			continue
		}
		if err := version.CheckReplica(r.Version, m.Version()); err != nil {
			return fmt.Sprintf("replica %s: %v", r.ID, err)
		}
	}
	return ""
}

// VersionHeader is the response header carrying tw-mgmt's version; the
// CLI compares it with its own (internal/version.CLIAdvice).
const VersionHeader = "Tw-Version"

func versionHeader(v string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, v)
		h.ServeHTTP(w, r)
	})
}
