package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/catalog"
	"github.com/rosskukulinski/tailworks/internal/version"
)

// Releases and upgrade runs (ADR-041 §8).
//
// The release catalog is the entries built into this binary
// (internal/catalog: this release and every earlier one) plus imported
// ones (POST /v1/releases), so a plan needs no network access. A fleet
// upgrade is a durable upgrade run in the store; every replica's ticker
// tries to take the run's lease ("upgrade/<id>") and the holder advances
// it one step at a time, so the run survives the CLI disconnecting and a
// replica dying.

// ReleaseEntry is a catalog entry as the API shows it.
type ReleaseEntry struct {
	catalog.Entry
	Source    string   `json:"source"` // builtin | imported
	Artifacts []string `json:"artifacts,omitempty"`
}

// Catalog lists every known release, ascending.
func (m *Mgmt) Catalog(ctx context.Context) ([]ReleaseEntry, error) {
	byV := map[string]ReleaseEntry{}
	for _, e := range catalog.Embedded() {
		byV[e.Version] = ReleaseEntry{Entry: e, Source: "builtin"}
	}
	if us, ok := m.store.(upgradeStore); ok {
		imp, err := us.ImportedReleases(ctx)
		if err != nil {
			return nil, err
		}
		for v, js := range imp {
			if _, builtin := byV[v]; builtin {
				continue
			}
			var e catalog.Entry
			if json.Unmarshal([]byte(js), &e) == nil {
				byV[v] = ReleaseEntry{Entry: e, Source: "imported"}
			}
		}
	}
	es := make([]catalog.Entry, 0, len(byV))
	for _, e := range byV {
		es = append(es, e.Entry)
	}
	catalog.Sort(es)
	out := make([]ReleaseEntry, 0, len(es))
	for _, e := range es {
		r := byV[e.Version]
		r.Artifacts = m.artifactNames(e.Version)
		out = append(out, r)
	}
	return out, nil
}

// ImportReleases stores imported catalog entries (built-in ones win).
func (m *Mgmt) ImportReleases(ctx context.Context, es []catalog.Entry, by string) error {
	us, ok := m.store.(upgradeStore)
	if !ok {
		return errf(501, "internal", "this store keeps no release catalog")
	}
	now := m.now().UnixMilli()
	var imported []string
	for _, e := range es {
		v, err := version.Parse(e.Version)
		if err != nil {
			return errf(400, "bad_request", "entry version: %v", err)
		}
		e.Version = v.String()
		if _, builtin := catalog.Find(catalog.Embedded(), e.Version); builtin {
			continue
		}
		b, _ := json.Marshal(e)
		if err := us.PutRelease(ctx, e.Version, string(b), by, now); err != nil {
			return err
		}
		imported = append(imported, e.Version)
	}
	m.audit(ctx, AuditRecord{Kind: "release_import", ActingFor: by}, map[string]any{"versions": imported})
	return nil
}

func entries(rs []ReleaseEntry) []catalog.Entry {
	out := make([]catalog.Entry, len(rs))
	for i, r := range rs {
		out[i] = r.Entry
	}
	return out
}

// ---- plans ----

// UpgradeRequest is POST /v1/upgrades's body.
type UpgradeRequest struct {
	To            string   `json:"to"`
	DryRun        bool     `json:"dryRun,omitempty"`
	Hosts         []string `json:"hosts,omitempty"`
	Canary        string   `json:"canary,omitempty"`
	SoakSeconds   *int     `json:"soakSeconds,omitempty"`
	SettleSeconds *int     `json:"settleSeconds,omitempty"`
}

// UpgradeStep is one step of a plan or run.
type UpgradeStep struct {
	Kind          string `json:"kind"` // backup | mgmt | contract | host
	Host          string `json:"host,omitempty"`
	Canary        bool   `json:"canary,omitempty"`
	Method        string `json:"method,omitempty"` // drain | bounce
	Status        string `json:"status"`           // pending | running | waiting | done | skipped | failed
	Detail        string `json:"detail,omitempty"`
	NeedsOperator string `json:"needsOperator,omitempty"`
	StartedAt     int64  `json:"startedAt,omitempty"`
	FinishedAt    int64  `json:"finishedAt,omitempty"`
	// Host step progress: cordon → drain → push → wait → settle → soak →
	// uncordon. PhaseAt is when the phase began (store clock, unix ms).
	Phase   string `json:"phase,omitempty"`
	PhaseAt int64  `json:"phaseAt,omitempty"`
	// SelfUpgrade: the host installs the release itself (push); otherwise
	// the operator does and the run waits for it to re-register.
	SelfUpgrade bool   `json:"selfUpgrade,omitempty"`
	Artifact    string `json:"artifact,omitempty"`
}

// UpgradePlan is what an upgrade to To would do.
type UpgradePlan struct {
	From struct {
		Mgmt  []string          `json:"mgmt"`
		Hosts map[string]string `json:"hosts"`
	} `json:"from"`
	To       string         `json:"to"`
	Path     []string       `json:"path,omitempty"`
	Steps    []UpgradeStep  `json:"steps"`
	Breaking []catalog.Note `json:"breaking"`
	Actions  []catalog.Note `json:"actions,omitempty"`
	Blockers []string       `json:"blockers"`
	Warnings []string       `json:"warnings"`
}

// Defaults for a run's gates.
const (
	DefaultUpgradeSoak   = 300 * time.Second
	DefaultUpgradeSettle = 300 * time.Second
)

// actionKinds are upgrade kinds whose notes need the operator.
var actionKinds = []string{"schema-contract", "workerd", "config-default", "compat-date"}

// PlanUpgrade plans the upgrade to req.To from the catalog and the fleet.
func (m *Mgmt) PlanUpgrade(ctx context.Context, req UpgradeRequest) (UpgradePlan, error) {
	var p UpgradePlan
	p.Breaking, p.Blockers, p.Warnings = []catalog.Note{}, []string{}, []string{}
	tv, err := version.Parse(req.To)
	if err != nil {
		return p, errf(400, "bad_request", "to: %v", err)
	}
	p.To = tv.String()
	cat, err := m.Catalog(ctx)
	if err != nil {
		return p, err
	}
	es := entries(cat)
	target, ok := catalog.Find(es, p.To)
	if !ok {
		return p, errf(400, "bad_request", "release %s is not in the catalog: import it first (tw upgrade import %s)", p.To, p.To)
	}

	// From: every live replica and every host in scope.
	hosts, err := m.store.ListHosts(ctx, Page{})
	if err != nil {
		return p, err
	}
	vs := m.versionStatus(ctx, hosts)
	for _, v := range vs.Mgmt {
		if !slices.Contains(p.From.Mgmt, v) {
			p.From.Mgmt = append(p.From.Mgmt, v)
		}
	}
	sort.Strings(p.From.Mgmt)
	p.From.Hosts = map[string]string{}
	var scope []Host
	for _, h := range hosts {
		if h.Pending() || len(req.Hosts) > 0 && !slices.Contains(req.Hosts, h.ID) {
			continue
		}
		p.From.Hosts[h.ID] = h.Version
		scope = append(scope, h)
	}
	for _, id := range req.Hosts {
		if _, ok := p.From.Hosts[id]; !ok {
			p.Blockers = append(p.Blockers, fmt.Sprintf("host %s is not registered", id))
		}
	}

	// The oldest version running decides the path.
	oldest := tv
	consider := func(v string) {
		if v == version.Dev {
			return
		}
		if v == "" {
			v = version.Legacy
		}
		if sv, err := version.Parse(v); err == nil && sv.Compare(oldest) < 0 {
			oldest = sv
		}
	}
	for _, v := range p.From.Mgmt {
		consider(v)
	}
	for _, h := range scope {
		consider(h.Version)
	}
	if mv, err := version.Parse(m.Version()); err == nil && tv.Compare(mv) < 0 {
		p.Blockers = append(p.Blockers, fmt.Sprintf("%s is older than this tw-mgmt (%s): a downgrade is a restore from the pre-upgrade backup, not an upgrade", p.To, mv))
	}
	between := catalog.Between(es, oldest.String(), p.To)
	if oldest.LinesBehind(tv) > 1 {
		hop := ""
		for _, e := range es {
			if ev, err := version.Parse(e.Version); err == nil && ev.LinesBehind(tv) == 1 {
				hop = ev.String() // the newest release on the line before the target
			}
		}
		if hop != "" {
			p.Path = append(p.Path, hop)
			p.Blockers = append(p.Blockers, fmt.Sprintf("parts of the fleet run %s, more than one release line behind %s: upgrade to %s first", oldest, p.To, hop))
		} else {
			p.Blockers = append(p.Blockers, fmt.Sprintf("parts of the fleet run %s, more than one release line behind %s, and the catalog has no release in between: import one", oldest, p.To))
		}
	}
	for _, e := range catalog.ContractSteps(between) {
		if e.Version != p.To && !slices.Contains(p.Path, e.Version) {
			p.Path = append(p.Path, e.Version)
			p.Blockers = append(p.Blockers, fmt.Sprintf("%s has contract migrations: upgrade to %s first, then to %s", e.Version, e.Version, p.To))
		}
	}
	sort.Slice(p.Path, func(i, j int) bool { return version.MustParse(p.Path[i]).Compare(version.MustParse(p.Path[j])) < 0 })
	p.Breaking = append(p.Breaking, catalog.Breaking(between)...)
	for _, e := range between {
		for _, n := range e.Notes {
			if n.Kind == "breaking" {
				continue
			}
			needs := strings.Contains(n.Text, "Operator action:")
			for _, k := range n.Upgrade {
				needs = needs || slices.Contains(actionKinds, k)
			}
			if needs {
				p.Actions = append(p.Actions, n)
			}
		}
	}

	// Steps.
	p.Steps = append(p.Steps, UpgradeStep{Kind: "backup", Status: "skipped",
		Detail: "mgmt can't verify a backup: before you start, back up the store (point-in-time recovery, or a dump) together with custody"})
	p.Warnings = append(p.Warnings, "take a store + custody backup before starting (restoring it is the only rollback once a contract migration ran)")
	ms := UpgradeStep{Kind: "mgmt", Status: "pending", Detail: "every live tw-mgmt replica must report " + p.To}
	for id, v := range vs.Mgmt {
		if !atLeast(v, tv) {
			ms.NeedsOperator = "roll every tw-mgmt replica to " + p.To + " with your deployment system; the run waits (replica " + id + " runs " + orLegacy(v) + ")"
			break
		}
	}
	p.Steps = append(p.Steps, ms)
	cs := UpgradeStep{Kind: "contract", Status: "skipped", Detail: "no contract migrations"}
	if len(target.ContractMigrations) > 0 || vs.Schema != nil && len(vs.Schema.PendingContract) > 0 {
		cs.Status, cs.Detail = "pending", "apply the deferred contract migrations once every replica runs "+p.To
	}
	p.Steps = append(p.Steps, cs)

	sort.Slice(scope, func(i, j int) bool { return scope[i].ID < scope[j].ID })
	canary := req.Canary
	if canary != "" && !slices.ContainsFunc(scope, func(h Host) bool { return h.ID == canary }) {
		p.Blockers = append(p.Blockers, fmt.Sprintf("canary %s is not a host in scope", canary))
	}
	now := m.now()
	var hostSteps []UpgradeStep
	for _, h := range scope {
		st := UpgradeStep{Kind: "host", Host: h.ID, Status: "pending"}
		if atLeast(h.Version, tv) {
			st.Status, st.Detail = "skipped", "already runs "+h.Version
			hostSteps = append(hostSteps, st)
			continue
		}
		if canary == "" {
			canary = h.ID
		}
		st.Canary = h.ID == canary
		st.Method = m.hostMethod(ctx, h)
		st.SelfUpgrade = slices.Contains(h.Features, "self-upgrade") && h.OS != ""
		if st.SelfUpgrade {
			st.Artifact = fmt.Sprintf("tailworks-%s-%s-%s.tar.gz", p.To, h.OS, h.Arch)
			if !slices.Contains(m.artifactNames(p.To), st.Artifact) {
				p.Blockers = append(p.Blockers, fmt.Sprintf("host %s upgrades itself but this tw-mgmt has no %s: tw upgrade import %s", h.ID, st.Artifact, p.To))
			}
			st.Detail = "the host installs " + st.Artifact + " and restarts"
		} else {
			st.NeedsOperator = fmt.Sprintf("install tw %s on host %s and restart tw-hostd (it can't upgrade itself: run tw-hostd with -self-upgrade under a service manager)", p.To, h.ID)
		}
		if !h.Live(now) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("host %s is not live: its step will wait for it", h.ID))
		}
		if slices.Contains(h.Tiers, "microvm") && target.Protocols["helper"] > h.Protocols["helper"] && h.Protocols["helper"] > 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("host %s: the vmhelper protocol changes (%d → %d): run `sudo tw host setup --microvm` on it as part of its upgrade", h.ID, h.Protocols["helper"], target.Protocols["helper"]))
		}
		if st.Method == "bounce" {
			st.Detail += "; workloads with host-local state restart in place (about a minute of unavailability)"
		}
		hostSteps = append(hostSteps, st)
	}
	// The canary goes first.
	sort.SliceStable(hostSteps, func(i, j int) bool { return hostSteps[i].Canary && !hostSteps[j].Canary })
	p.Steps = append(p.Steps, hostSteps...)
	return p, nil
}

// hostMethod: drain when every workload on h can move (stateless), else
// bounce in place.
func (m *Mgmt) hostMethod(ctx context.Context, h Host) string {
	ds, err := m.store.Assignments(ctx, h.ID)
	if err != nil {
		return "bounce"
	}
	for _, d := range ds {
		if d.Fence {
			return "bounce"
		}
	}
	return "drain"
}

func atLeast(v string, t version.Semver) bool {
	if v == version.Dev {
		return true
	}
	if v == "" {
		v = version.Legacy
	}
	sv, err := version.Parse(v)
	return err == nil && sv.Compare(t) >= 0
}

func orLegacy(v string) string {
	if v == "" {
		return version.Legacy + " (no version reported)"
	}
	return v
}

// ---- runs ----

// UpgradeRun is a durable fleet upgrade.
type UpgradeRun struct {
	ID        string        `json:"id"`
	To        string        `json:"to"`
	State     string        `json:"state"` // running | paused | done | aborted | failed
	Reason    string        `json:"reason,omitempty"`
	Step      int           `json:"step"`
	Steps     []UpgradeStep `json:"steps"`
	Plan      *UpgradePlan  `json:"plan,omitempty"`
	CreatedBy string        `json:"createdBy,omitempty"`
	CreatedAt int64         `json:"createdAt"`
	UpdatedAt int64         `json:"updatedAt"`
	// Gates (seconds).
	SoakSeconds   int `json:"soakSeconds"`
	SettleSeconds int `json:"settleSeconds"`
}

// StartUpgrade creates a run from a plan without blockers.
func (m *Mgmt) StartUpgrade(ctx context.Context, req UpgradeRequest, by string) (UpgradeRun, error) {
	us, ok := m.store.(upgradeStore)
	if !ok {
		return UpgradeRun{}, errf(501, "internal", "this store keeps no upgrade runs")
	}
	p, err := m.PlanUpgrade(ctx, req)
	if err != nil {
		return UpgradeRun{}, err
	}
	if len(p.Blockers) > 0 {
		return UpgradeRun{}, errf(409, "upgrade_blocked", "the upgrade can't start: %s", strings.Join(p.Blockers, "; "))
	}
	now := m.now().UnixMilli()
	run := UpgradeRun{ID: newID("up_"), To: p.To, State: "running", Steps: p.Steps, Plan: &p, CreatedBy: by,
		CreatedAt: now, UpdatedAt: now, SoakSeconds: int(DefaultUpgradeSoak / time.Second), SettleSeconds: int(DefaultUpgradeSettle / time.Second)}
	if req.SoakSeconds != nil {
		run.SoakSeconds = *req.SoakSeconds
	}
	if req.SettleSeconds != nil {
		run.SettleSeconds = *req.SettleSeconds
	}
	b, _ := json.Marshal(run)
	if err := us.CreateUpgradeRun(ctx, run.ID, run.To, run.State, string(b), now); err != nil {
		if errors.Is(err, ErrExists) {
			return UpgradeRun{}, errf(409, "exists", "an upgrade run is already active: tw upgrade fleet status")
		}
		return UpgradeRun{}, err
	}
	m.audit(ctx, AuditRecord{Kind: "upgrade_start", ActingFor: by}, map[string]any{"run": run.ID, "to": run.To, "steps": len(run.Steps)})
	m.kickUpgrades()
	return run, nil
}

// GetUpgrade reads a run.
func (m *Mgmt) GetUpgrade(ctx context.Context, id string) (UpgradeRun, int64, error) {
	us, ok := m.store.(upgradeStore)
	if !ok {
		return UpgradeRun{}, 0, errf(404, "not_found", "no upgrade runs")
	}
	js, at, err := us.GetUpgradeRun(ctx, id)
	if err != nil {
		return UpgradeRun{}, 0, lookupErr(err, errf(404, "not_found", "upgrade run %q not found", id))
	}
	var run UpgradeRun
	if err := json.Unmarshal([]byte(js), &run); err != nil {
		return UpgradeRun{}, 0, err
	}
	return run, at, nil
}

// ListUpgrades lists runs, newest first.
func (m *Mgmt) ListUpgrades(ctx context.Context) ([]UpgradeRun, error) {
	us, ok := m.store.(upgradeStore)
	if !ok {
		return []UpgradeRun{}, nil
	}
	js, err := us.ListUpgradeRuns(ctx, 50)
	if err != nil {
		return nil, err
	}
	out := []UpgradeRun{}
	for _, j := range js {
		var r UpgradeRun
		if json.Unmarshal([]byte(j), &r) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// ActOnUpgrade pauses, resumes or aborts a run.
func (m *Mgmt) ActOnUpgrade(ctx context.Context, id, action, by string) (UpgradeRun, error) {
	for range 5 {
		run, at, err := m.GetUpgrade(ctx, id)
		if err != nil {
			return run, err
		}
		switch action {
		case "pause":
			if run.State != "running" {
				return run, errf(409, "upgrade_state", "run %s is %s, not running", id, run.State)
			}
			run.State, run.Reason = "paused", "paused by "+by
		case "resume":
			if run.State != "paused" {
				return run, errf(409, "upgrade_state", "run %s is %s, not paused", id, run.State)
			}
			run.State, run.Reason = "running", ""
			if run.Step < len(run.Steps) {
				run.Steps[run.Step].PhaseAt = m.now().UnixMilli() // restart the step's timer
			}
		case "abort":
			if run.State != "running" && run.State != "paused" {
				return run, errf(409, "upgrade_state", "run %s is %s", id, run.State)
			}
			run.State, run.Reason = "aborted", "aborted by "+by
			if run.Step < len(run.Steps) {
				st := &run.Steps[run.Step]
				if st.Kind == "host" && st.Phase != "" {
					if h, err := m.store.GetHost(ctx, st.Host); err == nil && h.CordonedBy == "upgrade:"+run.ID {
						m.CordonHost(ctx, st.Host, false, "upgrade:"+run.ID)
					}
				}
				st.Status = "failed"
				st.Detail = "aborted"
			}
		default:
			return run, errf(400, "bad_request", "action must be pause, resume or abort")
		}
		err = m.saveRun(ctx, &run, at)
		if errors.Is(err, errStoreConflict) {
			continue // the driver advanced it meanwhile: re-read and retry
		}
		if err != nil {
			return run, err
		}
		m.audit(ctx, AuditRecord{Kind: "upgrade_" + action, ActingFor: by}, map[string]any{"run": id})
		if action == "resume" {
			m.kickUpgrades()
		}
		return run, nil
	}
	return UpgradeRun{}, errf(409, "store_conflict", "the run kept changing; retry")
}

func (m *Mgmt) saveRun(ctx context.Context, run *UpgradeRun, prev int64) error {
	us := m.store.(upgradeStore)
	run.UpdatedAt = m.now().UnixMilli()
	if run.UpdatedAt <= prev {
		run.UpdatedAt = prev + 1
	}
	b, _ := json.Marshal(run)
	return us.UpdateUpgradeRun(ctx, run.ID, run.State, string(b), prev, run.UpdatedAt)
}

// UpgradeTick is how often the driver advances runs.
var UpgradeTick = 3 * time.Second

// kickUpgrades wakes the driver.
func (m *Mgmt) kickUpgrades() {
	select {
	case m.upgradeKick <- struct{}{}:
	default:
	}
}

// upgradeLoop drives active runs (started by Start).
func (m *Mgmt) upgradeLoop() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.upgradeKick:
		case <-m.clock.After(UpgradeTick):
		}
		m.driveUpgrades(m.ctx)
	}
}

// driveUpgrades advances every active run this replica can lease.
func (m *Mgmt) driveUpgrades(ctx context.Context) {
	us, ok := m.store.(upgradeStore)
	if !ok {
		return
	}
	js, err := us.ActiveUpgradeRuns(ctx)
	if err != nil || len(js) == 0 {
		return
	}
	for _, j := range js {
		var run UpgradeRun
		if json.Unmarshal([]byte(j), &run) != nil || run.State != "running" {
			continue
		}
		lease := "upgrade/" + run.ID
		if _, err := m.store.AcquireLease(ctx, lease, m.replicaID, 30*time.Second, m.now()); err != nil {
			continue // another replica drives it
		}
		for range 8 { // several quick steps per tick
			cur, at, err := m.GetUpgrade(ctx, run.ID)
			if err != nil || cur.State != "running" {
				break
			}
			advanced := m.advanceRun(ctx, &cur)
			if err := m.saveRun(ctx, &cur, at); err != nil || !advanced {
				break
			}
		}
	}
}

// advanceRun moves run forward; it reports whether a step completed (so
// the caller may try the next one at once).
func (m *Mgmt) advanceRun(ctx context.Context, run *UpgradeRun) bool {
	now := m.now().UnixMilli()
	for run.Step < len(run.Steps) && (run.Steps[run.Step].Status == "skipped" || run.Steps[run.Step].Status == "done") {
		run.Step++
	}
	if run.Step >= len(run.Steps) {
		run.State, run.Reason = "done", ""
		m.audit(ctx, AuditRecord{Kind: "upgrade_step", ActingFor: "upgrade:" + run.ID}, map[string]any{"run": run.ID, "state": "done"})
		return false
	}
	st := &run.Steps[run.Step]
	if st.Status == "pending" {
		st.Status, st.StartedAt, st.PhaseAt = "running", now, now
	}
	done, err := m.advanceStep(ctx, run, st, now)
	if err != nil {
		run.State, run.Reason = "paused", err.Error()
		st.Detail = err.Error()
		m.audit(ctx, AuditRecord{Kind: "upgrade_pause", ActingFor: "upgrade:" + run.ID}, map[string]any{"run": run.ID, "step": run.Step, "reason": err.Error()})
		return false
	}
	if !done {
		return false
	}
	st.Status, st.FinishedAt, st.NeedsOperator = "done", now, ""
	m.audit(ctx, AuditRecord{Kind: "upgrade_step", ActingFor: "upgrade:" + run.ID},
		map[string]any{"run": run.ID, "step": run.Step, "kind": st.Kind, "host": st.Host})
	run.Step++
	if run.Step >= len(run.Steps) {
		run.State, run.Reason = "done", ""
	}
	return true
}

func (m *Mgmt) advanceStep(ctx context.Context, run *UpgradeRun, st *UpgradeStep, now int64) (bool, error) {
	tv := version.MustParse(run.To)
	switch st.Kind {
	case "backup":
		return true, nil
	case "mgmt":
		hosts, _ := m.store.ListHosts(ctx, Page{})
		for id, v := range m.versionStatus(ctx, hosts).Mgmt {
			if !atLeast(v, tv) {
				st.Status = "waiting"
				st.NeedsOperator = fmt.Sprintf("roll every tw-mgmt replica to %s (replica %s runs %s)", run.To, id, orLegacy(v))
				return false, nil
			}
		}
		return true, nil
	case "contract":
		us := m.store.(upgradeStore)
		ids, err := us.ApplyDeferredContract(ctx)
		if err != nil {
			return false, fmt.Errorf("contract migrations: %w", err)
		}
		st.Detail = "applied " + strings.Join(ids, ", ")
		if len(ids) == 0 {
			st.Detail = "nothing to apply"
		}
		return true, nil
	case "host":
		return m.advanceHost(ctx, run, st, now, tv)
	}
	return false, fmt.Errorf("unknown step kind %q", st.Kind)
}

func (m *Mgmt) advanceHost(ctx context.Context, run *UpgradeRun, st *UpgradeStep, now int64, tv version.Semver) (bool, error) {
	by := "upgrade:" + run.ID
	h, err := m.store.GetHost(ctx, st.Host)
	if err != nil {
		return false, fmt.Errorf("host %s: %v", st.Host, err)
	}
	settle := int64(run.SettleSeconds) * 1000
	phase := func(p string) {
		st.Phase, st.PhaseAt = p, now
	}
	switch st.Phase {
	case "":
		if atLeast(h.Version, tv) && h.CordonedAt == 0 {
			st.Detail = "already runs " + h.Version
			return true, nil
		}
		if _, err := m.CordonHost(ctx, h.ID, true, by); err != nil {
			return false, err
		}
		if st.Method == "drain" {
			phase("drain")
		} else {
			phase("push")
		}
		return false, nil
	case "drain":
		res, err := m.DrainHost(ctx, h.ID, DrainOptions{}, by)
		if err != nil {
			return false, fmt.Errorf("drain %s: %v", h.ID, err)
		}
		st.Detail = fmt.Sprintf("drained: %d moved, %d stay (bounce)", len(res.Moved), len(res.Refused))
		phase("push")
		return false, nil
	case "push":
		if st.SelfUpgrade {
			body := map[string]any{"runID": run.ID, "version": run.To, "name": st.Artifact,
				"url": "/v1/releases/" + run.To + "/artifacts/" + st.Artifact, "sha256": m.artifactSum(run.To, st.Artifact)}
			if err := m.pushJSON(ctx, h.ID, "/v1/push/upgrade", body, nil); err != nil {
				if now-st.PhaseAt > settle {
					return false, fmt.Errorf("host %s: the upgrade push failed for %ds: %v", h.ID, run.SettleSeconds, err)
				}
				st.Detail = "push failed, retrying: " + err.Error()
				return false, nil
			}
			st.Detail = "pushed " + st.Artifact
		} else {
			st.Status = "waiting"
			st.NeedsOperator = fmt.Sprintf("install tw %s on host %s and restart tw-hostd; the run continues when it re-registers", run.To, h.ID)
		}
		phase("wait")
		return false, nil
	case "wait":
		if atLeast(h.Version, tv) && h.Live(m.now()) && h.UpdatedAt >= st.PhaseAt {
			st.Status, st.NeedsOperator = "running", ""
			phase("settle")
			return false, nil
		}
		if st.SelfUpgrade && now-st.PhaseAt > settle {
			return false, fmt.Errorf("host %s did not re-register at %s within %ds (it runs %s)", h.ID, run.To, run.SettleSeconds, orLegacy(h.Version))
		}
		return false, nil
	case "settle":
		ok, why := m.hostSettled(ctx, h)
		if !ok {
			if now-st.PhaseAt > settle {
				return false, fmt.Errorf("host %s did not settle within %ds: %s", h.ID, run.SettleSeconds, why)
			}
			st.Detail = "settling: " + why
			return false, nil
		}
		if st.Canary && run.SoakSeconds > 0 {
			phase("soak")
			st.Detail = fmt.Sprintf("canary soak (%ds)", run.SoakSeconds)
			return false, nil
		}
		phase("uncordon")
		return false, nil
	case "soak":
		if ok, why := m.hostSettled(ctx, h); !ok {
			return false, fmt.Errorf("canary %s failed its soak: %s", h.ID, why)
		}
		if now-st.PhaseAt < int64(run.SoakSeconds)*1000 {
			return false, nil
		}
		phase("uncordon")
		return false, nil
	case "uncordon":
		if h.CordonedBy == by {
			if _, err := m.CordonHost(ctx, h.ID, false, by); err != nil {
				return false, err
			}
		}
		st.Detail = "runs " + h.Version
		return true, nil
	}
	return false, fmt.Errorf("unknown phase %q", st.Phase)
}

// hostSettled: the host is live and every deployment assigned to it is
// active.
func (m *Mgmt) hostSettled(ctx context.Context, h Host) (bool, string) {
	if !h.Live(m.now()) {
		return false, "not live"
	}
	ds, err := m.store.Assignments(ctx, h.ID)
	if err != nil {
		return false, err.Error()
	}
	for _, d := range ds {
		st, errText := m.deploymentStatus(ctx, d)
		if st == "failed" {
			return false, fmt.Sprintf("workload %s/%s failed: %s", d.TenantID, d.Workload, errText)
		}
		if st != "active" {
			return false, fmt.Sprintf("workload %s/%s is %s", d.TenantID, d.Workload, st)
		}
	}
	return true, ""
}

// ---- artifacts ----

func (m *Mgmt) releaseDir(v string) string {
	if m.cfg.ReleaseDir == "" {
		return ""
	}
	return filepath.Join(m.cfg.ReleaseDir, v)
}

func (m *Mgmt) artifactNames(v string) []string {
	dir := m.releaseDir(v)
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// artifactSum is name's SHA-256 from the release's SHA256SUMS ("" = none).
func (m *Mgmt) artifactSum(v, name string) string {
	b, err := os.ReadFile(filepath.Join(m.releaseDir(v), "SHA256SUMS"))
	if err != nil {
		return ""
	}
	return sumFor(b, name)
}

func sumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0]
		}
	}
	return ""
}
