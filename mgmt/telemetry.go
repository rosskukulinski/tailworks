package mgmt

// A tenant's OTLP export (ADR-045). The config, collector
// credentials (header values) included, lives only in custody, one entry
// per tenant, like a worker secret (ADR-034); the store holds nothing. No
// admin operation returns a header value. Hosts get the whole config
// through a grant fenced like a secret grant: only the holder of a
// workload's lease at its epoch, for exporting that workload.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rosskukulinski/tailworks/internal/obs"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Telemetry config limits.
const (
	TelemetryMaxHeaders     = 16
	TelemetryMaxHeaderValue = 4 << 10
	TelemetryMaxEndpoint    = 2048
)

// headerNameRE is an HTTP field name (token).
var headerNameRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")

// telemetryReservedHeaders are set by the exporter itself.
var telemetryReservedHeaders = map[string]bool{"host": true, "content-type": true, "content-length": true, "content-encoding": true,
	"transfer-encoding": true, "connection": true, "traceparent": true}

// OTLPExport is the tenant's OTLP export as custody holds it.
type OTLPExport struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
	Signals  []string          `json:"signals"`
}

// String never prints header values.
func (o OTLPExport) String() string {
	return fmt.Sprintf("OTLPExport{%s, headers %v [REDACTED], %v}", o.Endpoint, o.headerNames(), o.Signals)
}

func (o OTLPExport) headerNames() []string {
	out := make([]string, 0, len(o.Headers))
	for k := range o.Headers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// telemetryEntry is the custody entry.
type telemetryEntry struct {
	OTLP      *OTLPExport `json:"otlp,omitempty"`
	UpdatedAt int64       `json:"updatedAt"`
	UpdatedBy string      `json:"updatedBy,omitempty"`
}

// TelemetryView is GET's answer: header names only.
type TelemetryView struct {
	TenantID  string          `json:"tenantID"`
	OTLP      *OTLPExportView `json:"otlp,omitempty"`
	UpdatedAt int64           `json:"updatedAt,omitempty"`
	UpdatedBy string          `json:"updatedBy,omitempty"`
}

// OTLPExportView is an OTLPExport without header values.
type OTLPExportView struct {
	Endpoint    string   `json:"endpoint"`
	HeaderNames []string `json:"headerNames"`
	Signals     []string `json:"signals"`
}

// TelemetryGrant is the host's grant. It never prints header values.
type TelemetryGrant struct {
	TenantID  string      `json:"tenantID"`
	Workload  string      `json:"workload"`
	Epoch     int64       `json:"epoch"`
	OTLP      *OTLPExport `json:"otlp,omitempty"`
	GrantedAt int64       `json:"grantedAt"`
}

func (g TelemetryGrant) String() string {
	o := "none"
	if g.OTLP != nil {
		o = g.OTLP.String()
	}
	return fmt.Sprintf("TelemetryGrant{%s/%s epoch %d, %s}", g.TenantID, g.Workload, g.Epoch, o)
}

var telemetryMu sync.Mutex

func telemetryCustodyName(tenantID string) string { return "tw/" + tenantID + "/telemetry" }

// ValidateOTLPExport checks and normalises o (signals default to all,
// header names lowercase).
func ValidateOTLPExport(o *OTLPExport) error {
	if o == nil {
		return errf(400, "bad_request", "otlp is required")
	}
	if len(o.Endpoint) > TelemetryMaxEndpoint {
		return errf(400, "bad_request", "endpoint is longer than %d bytes", TelemetryMaxEndpoint)
	}
	if err := obs.ValidateEndpoint(o.Endpoint); err != nil {
		return errf(400, "bad_request", "otlp.endpoint: %v", err)
	}
	if len(o.Headers) > TelemetryMaxHeaders {
		return errf(400, "bad_request", "at most %d headers", TelemetryMaxHeaders)
	}
	hs := make(map[string]string, len(o.Headers))
	for k, v := range o.Headers {
		lk := strings.ToLower(k)
		if !headerNameRE.MatchString(k) || telemetryReservedHeaders[lk] {
			return errf(400, "bad_request", "header %q is not a valid (or allowed) header name", k)
		}
		if len(v) > TelemetryMaxHeaderValue || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n\x00") {
			// Never echo the value.
			return errf(400, "bad_request", "the value of header %q must be one line of UTF-8 text, at most %d bytes", k, TelemetryMaxHeaderValue)
		}
		if _, dup := hs[lk]; dup {
			return errf(400, "bad_request", "header %q is given twice", k)
		}
		hs[lk] = v
	}
	o.Headers = hs
	if len(o.Signals) == 0 {
		o.Signals = append([]string(nil), obs.AllSignals...)
	}
	seen := map[string]bool{}
	for _, s := range o.Signals {
		if !slices.Contains(obs.AllSignals, s) {
			return errf(400, "bad_request", "unknown signal %q (logs, metrics, traces)", s)
		}
		seen[s] = true
	}
	o.Signals = o.Signals[:0]
	for _, s := range obs.AllSignals {
		if seen[s] {
			o.Signals = append(o.Signals, s)
		}
	}
	return nil
}

func (m *Mgmt) loadTelemetry(ctx context.Context, tenantID string) (*telemetryEntry, error) {
	b, err := m.custody.Get(ctx, telemetryCustodyName(tenantID))
	if errors.Is(err, ErrNotFound) {
		return &telemetryEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("custody: %w", err)
	}
	var e telemetryEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, errors.New("custody: telemetry config does not decode") // never echo the content
	}
	return &e, nil
}

func (m *Mgmt) checkTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" || strings.ContainsAny(tenantID, "/\x00") {
		return errf(400, "bad_request", "bad tenant ID")
	}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	return nil
}

// Telemetry returns the tenant's config without header values.
func (m *Mgmt) Telemetry(ctx context.Context, tenantID string) (TelemetryView, error) {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return TelemetryView{}, err
	}
	e, err := m.loadTelemetry(ctx, tenantID)
	if err != nil {
		return TelemetryView{}, err
	}
	return e.view(tenantID), nil
}

func (e *telemetryEntry) view(tenantID string) TelemetryView {
	v := TelemetryView{TenantID: tenantID, UpdatedAt: e.UpdatedAt, UpdatedBy: e.UpdatedBy}
	if e.OTLP != nil {
		v.OTLP = &OTLPExportView{Endpoint: e.OTLP.Endpoint, HeaderNames: e.OTLP.headerNames(), Signals: e.OTLP.Signals}
	}
	return v
}

// PutTelemetry sets the tenant's OTLP export. Audited with the endpoint
// and header names, never values.
func (m *Mgmt) PutTelemetry(ctx context.Context, tenantID string, o OTLPExport, by string) (TelemetryView, error) {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return TelemetryView{}, err
	}
	if err := ValidateOTLPExport(&o); err != nil {
		return TelemetryView{}, err
	}
	telemetryMu.Lock()
	defer telemetryMu.Unlock()
	e := &telemetryEntry{OTLP: &o, UpdatedAt: m.now().UnixMilli(), UpdatedBy: by}
	b, err := json.Marshal(e)
	if err != nil {
		return TelemetryView{}, err
	}
	if err := m.custody.Put(ctx, telemetryCustodyName(tenantID), b); err != nil {
		return TelemetryView{}, fmt.Errorf("custody: %w", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "telemetry_put", ActingFor: by},
		map[string]any{"endpoint": o.Endpoint, "headers": o.headerNames(), "signals": o.Signals, "by": by})
	return e.view(tenantID), nil
}

// DeleteTelemetry stops the tenant's export (idempotent).
func (m *Mgmt) DeleteTelemetry(ctx context.Context, tenantID, by string) error {
	if err := m.checkTenant(ctx, tenantID); err != nil {
		return err
	}
	telemetryMu.Lock()
	defer telemetryMu.Unlock()
	if err := m.custody.Delete(ctx, telemetryCustodyName(tenantID)); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("custody: %w", err)
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "telemetry_delete", ActingFor: by}, map[string]any{"by": by})
	return nil
}

// telemetryReleased remembers the config version each host last got per
// workload, so a release is audited once per version, not every minute.
var telemetryReleased sync.Map // host|tenant|workload → updatedAt

// ReleaseTelemetry is the host's grant: the tenant's config, header values
// included, only to the holder of workload's lease at epoch (hostID "" on
// the local-dev socket skips the holder check, as for secrets). Audited
// (telemetry_release) the first time a host gets each version of the
// config for a workload; a refusal always is.
func (m *Mgmt) ReleaseTelemetry(ctx context.Context, tenantID, workload, hostID string, epoch int64) (*OTLPExport, error) {
	if tenantID == "" || workload == "" || strings.ContainsAny(tenantID+workload, "/\x00") {
		return nil, errf(400, "bad_request", "tenant and workload are required")
	}
	if epoch <= 0 {
		return nil, errf(400, "bad_request", "epoch query parameter must be positive")
	}
	l, err := m.store.GetLease(ctx, LeaseName(tenantID, workload))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, errf(409, "lease_lost", "no lease for %s/%s", tenantID, workload)
		}
		return nil, err
	}
	if l.Epoch != epoch || (hostID != "" && l.Holder != hostID) || l.ExpiresAt < m.now().UnixMilli() {
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "deny", Code: "lease_lost"},
			map[string]any{"host": hostID, "epoch": epoch, "telemetry": workload})
		return nil, errf(409, "lease_lost", "the lease on %s/%s is not held by %q at epoch %d", tenantID, workload, hostID, epoch)
	}
	e, err := m.loadTelemetry(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if e.OTLP != nil {
		k := hostID + "|" + tenantID + "|" + workload
		if prev, ok := telemetryReleased.Swap(k, e.UpdatedAt); !ok || prev != e.UpdatedAt {
			m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "telemetry_release"},
				map[string]any{"host": hostID, "epoch": epoch, "workload": workload, "endpoint": e.OTLP.Endpoint, "headers": e.OTLP.headerNames()})
		}
	}
	return e.OTLP, nil
}

// ---- /v1 handlers ----

func (a *v1api) GetTenantTelemetry(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	v, err := a.s.M.Telemetry(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *v1api) PutTenantTelemetry(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		OTLP *OTLPExport `json:"otlp"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.OTLP == nil {
		// Never echo the decoder's error: it can quote a header value.
		writeErr(w, r, errf(400, "bad_request", `the body must be {"otlp": {"endpoint": "<url>", "headers": {…}, "signals": [...]}}`))
		return
	}
	v, err := a.s.M.PutTelemetry(r.Context(), id, *body.OTLP, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *v1api) DeleteTenantTelemetry(w http.ResponseWriter, r *http.Request, id v1.TenantID) {
	if err := a.s.M.DeleteTelemetry(r.Context(), id, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) CreateTelemetryGrant(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.CreateTelemetryGrantParams) {
	w.Header().Set("Cache-Control", "no-store")
	o, err := a.s.M.ReleaseTelemetry(r.Context(), id, workload, hostOf(r), params.Epoch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, TelemetryGrant{TenantID: id, Workload: workload, Epoch: params.Epoch, OTLP: o, GrantedAt: a.s.M.now().UnixMilli()})
}
