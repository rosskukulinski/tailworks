package fakemgmt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// Worker secrets (ADR-034), in memory: put/list/delete as an
// admin, and the lease-fenced grant hostd fetches when it starts a
// deployment. `tw dev` loads .dev.vars through these.

type fakeSecret struct {
	value            string
	created, updated time.Time
	updatedBy        string
}

func secretKey(r *http.Request) string {
	return mgmtapi.LeaseName(r.PathValue("id"), r.PathValue("workload"))
}

// SecretGrants counts secret grants (tests).
func (s *Server) SecretGrants() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secretGrants
}

func (s *Server) knownTenant(w http.ResponseWriter, r *http.Request) bool {
	if s.tenants[r.PathValue("id")] == nil {
		writeErr(w, 404, "unknown_tenant", "no such tenant")
		return false
	}
	return true
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Value *string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.Value == nil {
		writeErr(w, 400, "bad_request", `the body must be {"value": "<string>"}`)
		return
	}
	if !mgmt.ValidSecretName(name) || len(*body.Value) > mgmt.SecretMaxValueBytes {
		writeErr(w, 400, "bad_request", "bad secret name or value too large")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	if s.secrets == nil {
		s.secrets = map[string]map[string]*fakeSecret{}
	}
	k := secretKey(r)
	if s.secrets[k] == nil {
		s.secrets[k] = map[string]*fakeSecret{}
	}
	now := time.Now()
	sec, exists := s.secrets[k][name]
	if !exists {
		sec = &fakeSecret{created: now}
		s.secrets[k][name] = sec
	}
	sec.value, sec.updated, sec.updatedBy = *body.Value, now, r.Header.Get("Tw-Actor")
	status := 200
	if !exists {
		status = 201
	}
	writeJSON(w, status, s.secretMeta(r, name, sec))
}

func (s *Server) secretMeta(r *http.Request, name string, sec *fakeSecret) mgmtapi.SecretMeta {
	return mgmtapi.SecretMeta{TenantID: r.PathValue("id"), Workload: r.PathValue("workload"), Name: name,
		CreatedAt: ms(sec.created), UpdatedAt: ms(sec.updated), UpdatedBy: sec.updatedBy}
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	set := s.secrets[secretKey(r)]
	items := make([]mgmtapi.SecretMeta, 0, len(set))
	for n, sec := range set {
		items = append(items, s.secretMeta(r, n, sec))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": nil})
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.knownTenant(w, r) {
		return
	}
	set := s.secrets[secretKey(r)]
	if _, ok := set[r.PathValue("name")]; !ok {
		writeErr(w, 404, "unknown_secret", "no such secret")
		return
	}
	delete(set, r.PathValue("name"))
	w.WriteHeader(204)
}

func (s *Server) secretGrant(w http.ResponseWriter, r *http.Request) {
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	name := secretKey(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[name]
	if l == nil || l.epoch != epoch {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, fmt.Sprintf("lease %s is not held at epoch %d", name, epoch))
		return
	}
	if hid := r.Header.Get("Tw-Host-ID"); hid != "" && hid != l.host {
		writeErr(w, 409, mgmtapi.CodeLeaseLost, "host "+hid+" does not hold the lease")
		return
	}
	s.secretGrants++
	vals := map[string]string{}
	for n, sec := range s.secrets[name] {
		vals[n] = sec.value
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, mgmtapi.SecretGrant{TenantID: r.PathValue("id"), Workload: r.PathValue("workload"), Epoch: epoch, Secrets: vals, GrantedAt: ms(time.Now())})
}
