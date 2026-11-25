package mgmt

// Worker secrets (ADR-034): Cloudflare-compatible `env.NAME`
// values a tenant admin sets with `tw secret put`. Values live only in
// custody (encrypted at rest: FileCustody seals with AES-256-GCM, setec
// with its own keys), one entry per workload; the store holds nothing. No
// admin operation returns a value: they are released, like a DO volume
// key, only to the host holding the workload's lease at its epoch, when it
// starts a deployment. Values are never logged or audited (names are).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// Secret limits (Cloudflare's: 128 per Worker, 5 KiB per value).
const (
	SecretMaxPerWorkload = 128
	SecretMaxValueBytes  = 5 << 10
)

// secretNameRE: a JS identifier that is also an environment variable name.
var secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidSecretName reports whether name may name a secret: an identifier
// usable as both env.NAME (workerd) and an environment variable (runtime
// node), not in tailworks' reserved TW_ / __TW prefixes.
func ValidSecretName(name string) bool {
	return secretNameRE.MatchString(name) && !strings.HasPrefix(name, "TW_") && !strings.HasPrefix(name, "__TW")
}

// SecretMeta is a secret without its value (GET …/secrets, PUT's answer).
type SecretMeta struct {
	TenantID  string `json:"tenantID"`
	Workload  string `json:"workload"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// secretEntry is one secret as sealed in custody.
type secretEntry struct {
	Value     string `json:"value"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// String never prints the value.
func (e secretEntry) String() string {
	return fmt.Sprintf("secret{updated %d by %s}", e.UpdatedAt, e.UpdatedBy)
}

// secretsMu serialises read-modify-write of a workload's custody entry in
// this process when the store can't lock across replicas (lockSecrets).
var secretsMu sync.Mutex

// lockSecrets serialises read-modify-write of a workload's custody entry
// across every replica of the store (#578: replicas sharing a setec
// custody each had only their own mutex, and the later of two concurrent
// writes erased the other: a deleted secret came back).
func (m *Mgmt) lockSecrets(ctx context.Context, tenantID, workload string) (func(), error) {
	if nl, ok := m.store.(nameLocker); ok {
		return nl.LockName(ctx, secretsCustodyName(tenantID, workload))
	}
	secretsMu.Lock()
	return secretsMu.Unlock, nil
}

// secretsCustodyName is where a workload's secrets live in custody.
func secretsCustodyName(tenantID, workload string) string {
	return "tw/" + tenantID + "/workloads/" + workload + "/secrets"
}

func (m *Mgmt) checkSecretScope(ctx context.Context, tenantID, workload string) error {
	if !validName(workload) {
		return errf(400, "bad_request", "workload must be a DNS label ([a-z0-9-], ≤63)")
	}
	if tenantID == "" || strings.ContainsAny(tenantID, "/\x00") {
		return errf(400, "bad_request", "bad tenant ID")
	}
	if _, err := m.store.GetTenant(ctx, tenantID); err != nil {
		return lookupErr(err, errf(404, "unknown_tenant", "tenant %q not found", tenantID))
	}
	return nil
}

func (m *Mgmt) loadSecrets(ctx context.Context, tenantID, workload string) (map[string]secretEntry, error) {
	b, err := m.custody.Get(ctx, secretsCustodyName(tenantID, workload))
	if errors.Is(err, ErrNotFound) {
		return map[string]secretEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("custody: %w", err)
	}
	set := map[string]secretEntry{}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, errors.New("custody: workload secrets do not decode") // never echo the content
	}
	return set, nil
}

func (m *Mgmt) storeSecrets(ctx context.Context, tenantID, workload string, set map[string]secretEntry) error {
	name := secretsCustodyName(tenantID, workload)
	if len(set) == 0 {
		return m.custody.Delete(ctx, name)
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if err := m.custody.Put(ctx, name, b); err != nil {
		return fmt.Errorf("custody: %w", err)
	}
	return nil
}

// PutSecret creates or replaces a workload secret. created reports a new
// name. The value is never logged or audited.
func (m *Mgmt) PutSecret(ctx context.Context, tenantID, workload, name, value, by string) (SecretMeta, bool, error) {
	if err := m.checkSecretScope(ctx, tenantID, workload); err != nil {
		return SecretMeta{}, false, err
	}
	if !ValidSecretName(name) {
		return SecretMeta{}, false, errf(400, "bad_request", "secret name %q must match [A-Za-z_][A-Za-z0-9_]* (≤128) and not start with TW_ or __TW", name)
	}
	if len(value) > SecretMaxValueBytes {
		return SecretMeta{}, false, errf(400, "bad_request", "secret value is %d bytes; at most %d", len(value), SecretMaxValueBytes)
	}
	if !utf8.ValidString(value) {
		return SecretMeta{}, false, errf(400, "bad_request", "secret value must be UTF-8 text")
	}
	unlock, err := m.lockSecrets(ctx, tenantID, workload)
	if err != nil {
		return SecretMeta{}, false, err
	}
	defer unlock()
	set, err := m.loadSecrets(ctx, tenantID, workload)
	if err != nil {
		return SecretMeta{}, false, err
	}
	now := m.now().UnixMilli()
	e, exists := set[name]
	if !exists {
		if len(set) >= SecretMaxPerWorkload {
			return SecretMeta{}, false, errf(400, "too_many_secrets", "workload %s already has %d secrets (at most %d)", workload, len(set), SecretMaxPerWorkload)
		}
		e.CreatedAt = now
	}
	e.Value, e.UpdatedAt, e.UpdatedBy = value, now, by
	set[name] = e
	if err := m.storeSecrets(ctx, tenantID, workload, set); err != nil {
		return SecretMeta{}, false, err
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "secret_put", ActingFor: by},
		map[string]any{"workload": workload, "name": name, "by": by, "created": !exists})
	return SecretMeta{TenantID: tenantID, Workload: workload, Name: name, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, UpdatedBy: by}, !exists, nil
}

// DeleteSecret removes a workload secret (404 unknown_secret if absent).
func (m *Mgmt) DeleteSecret(ctx context.Context, tenantID, workload, name, by string) error {
	if err := m.checkSecretScope(ctx, tenantID, workload); err != nil {
		return err
	}
	unlock, err := m.lockSecrets(ctx, tenantID, workload)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := m.loadSecrets(ctx, tenantID, workload)
	if err != nil {
		return err
	}
	if _, ok := set[name]; !ok {
		return errf(404, "unknown_secret", "workload %s of tenant %s has no secret %q", workload, tenantID, name)
	}
	delete(set, name)
	if err := m.storeSecrets(ctx, tenantID, workload, set); err != nil {
		return err
	}
	m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "secret_delete", ActingFor: by},
		map[string]any{"workload": workload, "name": name, "by": by})
	return nil
}

// ListSecrets returns a workload's secrets, by name, without values.
func (m *Mgmt) ListSecrets(ctx context.Context, tenantID, workload string) ([]SecretMeta, error) {
	if err := m.checkSecretScope(ctx, tenantID, workload); err != nil {
		return nil, err
	}
	set, err := m.loadSecrets(ctx, tenantID, workload)
	if err != nil {
		return nil, err
	}
	out := make([]SecretMeta, 0, len(set))
	for n, e := range set {
		out = append(out, SecretMeta{TenantID: tenantID, Workload: workload, Name: n, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, UpdatedBy: e.UpdatedBy})
	}
	slices.SortFunc(out, func(a, b SecretMeta) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ReleaseSecrets is the secret grant: a workload's values, released only to
// the host holding its lease at epoch (hostID "" on the local-dev socket
// skips the holder check, as for volume keys). Audited, names only, when
// there is anything to release.
func (m *Mgmt) ReleaseSecrets(ctx context.Context, tenantID, workload, hostID string, epoch int64) (map[string]string, error) {
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
			map[string]any{"host": hostID, "epoch": epoch, "secrets": workload})
		return nil, errf(409, "lease_lost", "the lease on %s/%s is not held by %q at epoch %d", tenantID, workload, hostID, epoch)
	}
	// A replica ("<workload>--r2", its own lease) gets its group's secrets,
	// and a preview its workload's (preview.go): they are set on the
	// workload the user deploys.
	src, rel := workload, ""
	if g, k, err := m.secretsOwnerOf(ctx, tenantID, workload); err != nil {
		return nil, err
	} else if g != "" {
		src, rel = g, k
	}
	set, err := m.loadSecrets(ctx, tenantID, src)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(set))
	names := make([]string, 0, len(set))
	for n, e := range set {
		out[n] = e.Value
		names = append(names, n)
	}
	if len(names) > 0 {
		slices.Sort(names)
		detail := map[string]any{"host": hostID, "epoch": epoch, "workload": workload, "names": names}
		if src != workload {
			detail[rel] = src
		}
		m.audit(ctx, AuditRecord{TenantID: tenantID, Kind: "secret_release"}, detail)
	}
	return out, nil
}

// replicaGroupOf is the workload whose replica workload is ("" if it is
// not a replica), from its latest deployment.
func (m *Mgmt) replicaGroupOf(ctx context.Context, tenantID, workload string) (string, error) {
	ds, err := m.store.LatestDeployments(ctx, tenantID)
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if d.Workload == workload {
			return replicaGroup(d.Namespaces), nil
		}
	}
	return "", nil
}

// ---- /v1 handlers ----

func (a *v1api) ListWorkloadSecrets(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.ListWorkloadSecretsParams) {
	pg, n, err := pageParams(params.Limit, params.Cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	all, err := a.s.M.ListSecrets(r.Context(), id, workload)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rows := all[:0:0]
	for _, s := range all {
		if pg.After == nil || s.Name > pg.After.ID {
			rows = append(rows, s)
		}
	}
	if len(rows) > pg.Limit {
		rows = rows[:pg.Limit]
	}
	writeJSON(w, 200, mkPage(rows, n, func(s SecretMeta) PageKey { return PageKey{ID: s.Name} }))
}

func (a *v1api) PutWorkloadSecret(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.SecretName) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		Value *string `json:"value"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Value == nil {
		// Never echo the decoder's error: it can quote the value.
		writeErr(w, r, errf(400, "bad_request", `the body must be {"value": "<string>"}`))
		return
	}
	meta, created, err := a.s.M.PutSecret(r.Context(), id, workload, name, *body.Value, principalOf(r.Context()).Actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, meta)
}

func (a *v1api) DeleteWorkloadSecret(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, name v1.SecretName) {
	if err := a.s.M.DeleteSecret(r.Context(), id, workload, name, principalOf(r.Context()).Actor(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a *v1api) CreateWorkloadSecretGrant(w http.ResponseWriter, r *http.Request, id v1.TenantID, workload v1.WorkloadName, params v1.CreateWorkloadSecretGrantParams) {
	w.Header().Set("Cache-Control", "no-store")
	vals, err := a.s.M.ReleaseSecrets(r.Context(), id, workload, hostOf(r), params.Epoch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, SecretGrant{TenantID: id, Workload: workload, Epoch: params.Epoch, Secrets: vals, GrantedAt: a.s.M.now().UnixMilli()})
}

// SecretGrant is the answer to POST …/secret-grants. It never prints its
// values (%v, logs).
type SecretGrant struct {
	TenantID  string            `json:"tenantID"`
	Workload  string            `json:"workload"`
	Epoch     int64             `json:"epoch"`
	Secrets   map[string]string `json:"secrets"`
	GrantedAt int64             `json:"grantedAt"`
}

func (g SecretGrant) String() string {
	return fmt.Sprintf("SecretGrant{%s/%s epoch %d, %d secrets [REDACTED]}", g.TenantID, g.Workload, g.Epoch, len(g.Secrets))
}
