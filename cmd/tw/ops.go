package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/auditfmt"
	"github.com/rosskukulinski/tailworks/mgmt"
)

func nodeName(o mgmt.Object) string {
	if o.MagicDNS != "" {
		n, _, _ := strings.Cut(o.MagicDNS, ".")
		return n
	}
	return o.Hostname
}

func displayNS(o mgmt.Object) string {
	if o.Namespace == mgmt.WorkloadNamespace {
		return "workload " + o.Name
	}
	if o.Workload != "" {
		return o.Workload + ":" + o.Namespace + "/" + o.Name
	}
	return o.Namespace + "/" + o.Name
}

// resolveObject finds an object by ID, node name, MagicDNS name,
// "<Class>/<name>" or (if unique) name. Live objects win.
func resolveObject(objs []mgmt.Object, ref string) (mgmt.Object, error) {
	ref = strings.TrimSuffix(ref, ".")
	var hits []mgmt.Object
	for _, o := range objs {
		if o.ID == ref || o.Hostname == ref || o.MagicDNS == ref || nodeName(o) == ref || o.Namespace+"/"+o.Name == ref ||
			o.Workload+":"+o.Namespace+"/"+o.Name == ref ||
			(o.Namespace == mgmt.WorkloadNamespace && o.Name == ref) {
			hits = append(hits, o)
		}
	}
	if len(hits) == 0 {
		for _, o := range objs {
			if o.Name == ref {
				hits = append(hits, o)
			}
		}
	}
	if len(hits) > 1 {
		var live []mgmt.Object
		for _, o := range hits {
			if o.Live() {
				live = append(live, o)
			}
		}
		if len(live) > 0 {
			hits = live
		}
	}
	switch len(hits) {
	case 0:
		var names []string
		for _, o := range objs {
			if o.Live() {
				names = append(names, nodeName(o))
			}
		}
		sort.Strings(names)
		e := fail("unknown_object", fmt.Sprintf("no object %q", ref))
		if len(names) > 0 {
			e.Lines = append(e.Lines, "live objects: "+strings.Join(names, ", "))
		}
		return mgmt.Object{}, e.withFix("tw ls")
	case 1:
		return hits[0], nil
	}
	var ids []string
	for _, o := range hits {
		ids = append(ids, o.ID)
	}
	return mgmt.Object{}, fail("ambiguous_object", fmt.Sprintf("%q matches %d objects: %s", ref, len(hits), strings.Join(ids, ", "))).withFix("name it by object ID")
}

// lineage returns root … o.
func lineage(objs []mgmt.Object, o mgmt.Object) []mgmt.Object {
	by := map[string]mgmt.Object{}
	for _, x := range objs {
		by[x.ID] = x
	}
	out := []mgmt.Object{o}
	for cur := o; cur.ParentID != ""; {
		p, ok := by[cur.ParentID]
		if !ok || len(out) > 16 {
			break
		}
		out = append([]mgmt.Object{p}, out...)
		cur = p
	}
	return out
}

func descendants(objs []mgmt.Object, id string) []mgmt.Object {
	var out []mgmt.Object
	frontier := []string{id}
	for len(frontier) > 0 {
		var next []string
		for _, o := range objs {
			if slices.Contains(frontier, o.ParentID) {
				out = append(out, o)
				next = append(next, o.ID)
			}
		}
		frontier = next
	}
	return out
}

// principal returns who the root of o's lineage was created for (audit
// create/spawn records carry actingFor).
func (s *state) principal(ctx context.Context, root mgmt.Object) string {
	var ar struct {
		Records []mgmt.AuditRecord `json:"items"`
	}
	if s.mgmt().Do(ctx, "GET", "/v1/audit?limit=500&object="+url.QueryEscape(root.ID), nil, &ar) == nil {
		for _, r := range ar.Records {
			if (r.Kind == "create" || r.Kind == "spawn" || r.Kind == "principal") && r.ActingFor != "" {
				return r.ActingFor
			}
		}
	}
	return ""
}

func chainString(principal string, lin []mgmt.Object) string {
	var parts []string
	if principal != "" {
		parts = append(parts, principal)
	}
	for _, o := range lin {
		parts = append(parts, nodeName(o))
	}
	return strings.Join(parts, " → ")
}

// ---- ls ---- (ls.go)

// ---- kill ----

type killResult struct {
	Object      string             `json:"object"`
	Chain       string             `json:"chain"`
	Result      mgmt.KillResult    `json:"result"`
	Descendants int                `json:"descendants"`
	Timings     map[string]float64 `json:"timingsMs"`
}

func cmdKill(args []string) error {
	f := newFlags("kill")
	cascade := f.Bool("cascade", true, "also kill every descendant (sub-agents); --cascade=false leaves them to the reaper")
	reason := f.String("reason", "", "why (recorded in audit)")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw kill needs one object", "usage: tw "+usageOf("kill"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	objs, err := s.objectsOf(ctx, tn.TenantID)
	if err != nil {
		return err
	}
	o, err := resolveObject(objs, pos[0])
	if err != nil {
		return err
	}
	if o.TenantID != tn.TenantID { // defensive: mgmt filtered by tenant
		return fail("unknown_object", fmt.Sprintf("%s is not an object of tenant %s", pos[0], tn.Tailnet))
	}
	if !o.Live() {
		return fail("killed", fmt.Sprintf("%s is already %s (%s)", nodeName(o), o.Status, o.KillReason))
	}
	r := *reason
	if r == "" {
		r = "killed by " + actor()
	}
	ks := time.Now()
	var kr mgmt.KillResult
	q := url.Values{"reason": {r}, "cascade": {fmt.Sprint(*cascade)}}
	if err := s.mgmt().DoHeaders(ctx, "DELETE", "/v1/objects/"+o.ID+"?"+q.Encode(), nil,
		&kr, map[string]string{"Tw-Actor": actor()}); err != nil {
		return err
	}
	res := killResult{Object: o.ID, Result: kr, Timings: map[string]float64{"kill_api": ms(time.Since(ks))}}
	lin := lineage(objs, o)
	res.Chain = chainString(s.principal(ctx, lin[0]), lin)
	res.Descendants = len(kr.Killed) - 1
	res.Timings["total"] = ms(time.Since(t0))
	emit(f, res, func() {
		fmt.Printf("✓ tier 1  flows dropped, isolate stopped, node tombstoned (object_killed)   pushed %d/%d in %.0f ms\n", len(kr.Pushed), len(kr.Killed), kr.PushMS)
		switch {
		case len(kr.DevicesDeleting) > 0:
			fmt.Printf("  tier 2  %d device(s) deleted after the tombstone window (%s) · `tw down` flushes\n", len(kr.DevicesDeleting), s.Cfg.DeviceDeleteDelay)
		case len(kr.DevicesDeleted) > 0:
			fmt.Printf("✓ tier 2  %d device(s) deleted\n", len(kr.DevicesDeleted))
		}
		fmt.Printf("  tier 3  posture flip: not available (plan_feature_missing: custom posture attributes)\n")
		fmt.Printf("revoked: %s (+%d descendants)\n", tsafe(res.Chain), max(res.Descendants, 0))
		for id, e := range kr.Errors {
			fmt.Printf("  warning %s: %s\n", tsafe(id), tsafe(e))
		}
		for _, w := range kr.Warnings {
			fmt.Printf("› %s  %s (%s)\n  fix: %s\n", tsafe(w.Code), tsafe(w.Message), tsafe(strings.Join(w.Objects, ", ")), tsafe(w.Fix))
		}
	})
	return nil
}

// ---- audit ----

type auditLine struct {
	TS     string `json:"ts"`
	Tenant string `json:"tenantID"`
	Kind   string `json:"kind"`
	// Source is the record's provenance: mgmt, host, or tenant (a
	// tenant_note whose content is unverified tenant code output; RT-02).
	Source    string          `json:"source,omitempty"`
	Object    string          `json:"object"`
	Node      string          `json:"node"`
	Chain     string          `json:"chain"`
	ActingFor string          `json:"actingFor,omitempty"`
	Text      string          `json:"text"`
	Detail    json.RawMessage `json:"detail,omitempty"`
}

func detailMap(r mgmt.AuditRecord) (map[string]any, string) {
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
	return nil, ""
}

func describe(r mgmt.AuditRecord, names map[string]string) string {
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

// orAny returns the first non-nil value.
func orAny(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

// tenantNoteText renders a tenant_note: unverified content written by
// tenant code, never a platform decision, whatever it claims.
func tenantNoteText(r mgmt.AuditRecord) string {
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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type auditResult struct {
	Tenant   string             `json:"tenant"`
	TenantID string             `json:"tenantID"`
	Chain    string             `json:"chain,omitempty"`
	Lines    []auditLine        `json:"records"`
	Verified bool               `json:"verified"`
	Chains   []mgmt.ChainStatus `json:"chains"`

	// Truncated: the bare listing stopped at -n; older records exist.
	Truncated bool `json:"truncated,omitempty"`
}

func cmdAudit(args []string) error {
	f := newFlags("audit")
	human := f.String("human", "", "records acting for this principal (login or caller node)")
	trace := f.String("trace", "", "records of one trace")
	all := f.Bool("all", false, "every record of the tenant")
	// limits.md#audit-n
	n := f.Int("n", 50, "bare tw audit: the newest records (an object, --human, --trace and --all are always complete)")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	recent := len(pos) == 0 && *human == "" && *trace == "" && !*all // the newest -n records
	if len(pos) > 1 || recent && *n < 1 {
		return fail("bad_request", "tw audit takes one object, --human, --trace, --all, or nothing for the newest -n (≥ 1) records", "usage: tw "+usageOf("audit"))
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	objs, err := s.objectsOf(ctx, tn.TenantID)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, o := range objs {
		names[o.ID] = nodeName(o)
	}
	res := auditResult{Tenant: tn.Tailnet, TenantID: tn.TenantID, Lines: []auditLine{}, Verified: true}
	seen := map[string]bool{}
	var recs []mgmt.AuditRecord
	limit := 0 // > 0: stop after this many records (bare tw audit)
	query := func(q string) error {
		// Always scoped to the tenant: one tenant's audit never shows
		// another's. Every page is followed (up to limit).
		cursor := ""
		for {
			var ar struct {
				Records    []mgmt.AuditRecord `json:"items"`
				NextCursor *string            `json:"nextCursor"`
				Chains     []mgmt.ChainStatus `json:"chains"`
				Verified   bool               `json:"verified"`
			}
			pageN := 500
			if limit > 0 {
				pageN = min(pageN, limit-len(recs))
			}
			u := "/v1/audit?limit=" + fmt.Sprint(pageN) + "&tenant=" + url.QueryEscape(tn.TenantID)
			if q != "" {
				u += "&" + q
			}
			if cursor != "" {
				u += "&cursor=" + url.QueryEscape(cursor)
			}
			if err := s.mgmt().Do(ctx, "GET", u, nil, &ar); err != nil {
				return err
			}
			res.Verified = res.Verified && ar.Verified
			for _, c := range ar.Chains {
				if !slices.ContainsFunc(res.Chains, func(x mgmt.ChainStatus) bool { return x.Chain == c.Chain }) {
					res.Chains = append(res.Chains, c)
				}
			}
			for _, r := range ar.Records {
				k := fmt.Sprintf("%s/%d", r.Chain, r.Seq)
				if !seen[k] {
					seen[k] = true
					recs = append(recs, r)
				}
			}
			if ar.NextCursor == nil || *ar.NextCursor == "" {
				return nil
			}
			if limit > 0 && len(recs) >= limit {
				res.Truncated = true
				return nil
			}
			cursor = *ar.NextCursor
		}
	}
	switch {
	case len(pos) == 1:
		o, err := resolveObject(objs, pos[0])
		if err != nil {
			return err
		}
		lin := lineage(objs, o)
		res.Chain = chainString(s.principal(ctx, lin[0]), lin)
		for _, x := range append([]mgmt.Object{o}, descendants(objs, o.ID)...) {
			if err := query("object=" + url.QueryEscape(x.ID)); err != nil {
				return err
			}
		}
	case *human != "":
		if err := query("human=" + url.QueryEscape(*human)); err != nil {
			return err
		}
	case *all:
		if err := query(""); err != nil {
			return err
		}
	case recent:
		limit = *n
		if err := query("order=desc"); err != nil {
			return err
		}
		slices.Reverse(recs) // oldest first, like every other mode
	default:
		if err := query("trace=" + url.QueryEscape(*trace)); err != nil {
			return err
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].TS < recs[j].TS })
	for _, r := range recs {
		if r.TenantID != tn.TenantID {
			continue // defensive: mgmt filtered by tenant
		}
		text := describe(r, names)
		if r.Kind == mgmt.KindTenantNote || r.Source == mgmt.SourceTenant {
			text = tenantNoteText(r)
		}
		res.Lines = append(res.Lines, auditLine{TS: r.TS, Tenant: r.TenantID, Kind: r.Kind, Source: r.Source, Object: r.ObjectID, Node: names[r.ObjectID], Chain: r.Chain,
			ActingFor: r.ActingFor, Text: text, Detail: r.Detail})
	}
	emit(f, res, func() {
		if res.Chain != "" {
			fmt.Printf("chain: %s\n", tsafe(res.Chain))
		}
		for _, l := range res.Lines {
			ts := l.TS
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				ts = t.UTC().Format("2006-01-02T15:04:05.000Z")
			}
			src := ""
			if strings.HasPrefix(l.Chain, "host:") {
				src = " [" + strings.TrimPrefix(l.Chain, "host:") + "]"
			}
			kind := l.Kind
			if l.Source == mgmt.SourceTenant {
				kind = "~" + l.Kind // tenant-written content, not a platform record
			}
			fmt.Printf("%s  %-13s %-34s %s%s\n", tsafe(ts), tsafe(kind), tsafe(orDash(l.Node)), tsafe(l.Text), tsafe(src))
		}
		// Verification speaks up only when it fails: a verified trail is
		// one quiet line, plus any retention note (#406).
		var cs []string
		for _, c := range res.Chains {
			if res.Verified && !(c.OK && c.PrunedThrough > 0) {
				continue
			}
			st := "ok"
			if !c.OK {
				st = fmt.Sprintf("BROKEN at %d (%s)", c.BrokenAt, tsafe(c.Error))
			} else if strings.HasPrefix(c.Chain, "host:") {
				st = "signature ok"
			}
			if c.OK && c.PrunedThrough > 0 {
				st += fmt.Sprintf(" (from %d: retention pruned the older records)", c.PrunedThrough+1)
			}
			cs = append(cs, fmt.Sprintf("%s %s", tsafe(c.Chain), st))
		}
		if res.Verified {
			fmt.Printf("%d records · chain verified%s\n", len(res.Lines), strings.Join(append([]string{""}, cs...), " · "))
		} else {
			fmt.Printf("chain NOT VERIFIED: %d records · %s\n", len(res.Lines), strings.Join(cs, " · "))
		}
		if res.Truncated {
			fmt.Println("older records: tw audit --all (verifies every chain)")
		}
	})
	if !res.Verified {
		return fail("audit_unverified", "an audit chain failed verification")
	}
	return nil
}

// ---- logs ----

func cmdLogs(args []string) error {
	f := newFlags("logs")
	comp := f.String("component", "", "mgmt | hostd | supervisor: the daemons' logs")
	// limits.md#logs-n
	n := f.Int("n", 50, "lines")
	follow := f.Bool("f", false, "follow")
	tenantF := tenantFlag(f)
	hostF := f.String("host", "", "a replicated workload, from a linked machine: only its replica on this host")
	lopts := logsFlags(f) // logs_records.go
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	lo, err := lopts()
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if !s.remote() && !s.configured() {
		return fail("not_up", "tw is not set up in "+s.Dir)
	}
	if len(pos) == 0 && *comp == "" {
		w, err := defaultLogsWorkload(s, *tenantF) // logs_default.go: never a daemon's log
		if err != nil {
			return err
		}
		pos = []string{w}
	}
	if len(pos) != 1 && !lo.filter.Empty() {
		return fail("bad_request", "the record filters need a workload: tw logs <workload> --errors")
	}
	out, flush := logsOut(*f.json, pos, *comp, s.remote()) // agentjson.go: NDJSON with --json
	defer flush()
	if s.remote() {
		return remoteLogs(out, s, pos, *comp, *tenantF, *hostF, *n, *follow, lo) // logs_remote.go
	}
	var path string
	switch {
	case len(pos) == 1 && *comp == "":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var hv hostd.StatusView
		if err := s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv); err != nil {
			return fail("host_unavailable", "tw-hostd is not answering", err.Error()).withFix("tw status")
		}
		tn, err := s.tenant(*tenantF)
		if err != nil {
			return err
		}
		for _, w := range hv.Workloads {
			if w.TenantID != tn.TenantID || w.Workload != pos[0] {
				continue
			}
			if !lo.console && w.RequestLog != "" {
				return localRecords(w.RequestLog, pos[0], *n, *follow, lo.filter) // logs_records.go
			}
			if w.LogFile != "" {
				path = w.LogFile // workerd.log, or app.log for runtime "node"
			} else if w.InstanceDir != "" {
				path = filepath.Join(w.InstanceDir, "workerd.log")
			}
		}
		if path == "" {
			return fail("unknown_workload", "no running workload "+pos[0]).withFix("tw ls")
		}
	default:
		c := *comp
		if !slices.Contains([]string{"mgmt", "hostd", "supervisor"}, c) {
			return fail("bad_request", "unknown component "+c)
		}
		path = s.log(c)
	}
	fh, err := os.Open(path)
	if err != nil {
		return fail("no_logs", "no log at "+path)
	}
	defer fh.Close()
	b, _ := io.ReadAll(fh)
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) > *n {
		lines = lines[len(lines)-*n:]
	}
	for _, l := range lines {
		fmt.Fprintln(out, string(l))
	}
	if !*follow {
		return nil
	}
	rd := bufio.NewReader(fh)
	for {
		l, err := rd.ReadString('\n')
		if l != "" {
			fmt.Fprint(out, l)
		}
		if err != nil {
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// ---- undeploy ----

// cmdUndeploy is `tw undeploy <workload> [--tenant …] [--reason …]`:
// DELETE /v1/deployments/{id} of the workload's latest deployment: its
// objects are killed (cascade), the lease released, the deployment stopped.
func cmdUndeploy(args []string) error {
	f := newFlags("undeploy")
	reason := f.String("reason", "", "why (recorded in audit)")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw undeploy needs one workload", "usage: tw "+usageOf("undeploy"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*tenantF)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	deps, err := listAll[mgmt.DeploymentView](ctx, s.mgmt(), "/v1/deployments?latest=true&tenant="+url.QueryEscape(tn.TenantID)+"&workload="+url.QueryEscape(pos[0]), 0)
	if err != nil {
		return err
	}
	if len(deps) == 0 {
		return fail("unknown_workload", "no deployment of "+pos[0]+" for tenant "+tn.Tailnet).withFix("tw ls")
	}
	d := deps[len(deps)-1]
	if d.Status == "stopped" {
		return fail("already_stopped", pos[0]+" is already undeployed ("+d.DeploymentID+")")
	}
	q := url.Values{}
	if *reason != "" {
		q.Set("reason", *reason)
	}
	u := "/v1/deployments/" + d.DeploymentID
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	t0 := time.Now()
	var kr mgmt.KillResult
	if err := s.mgmt().DoHeaders(ctx, "DELETE", u, nil, &kr, map[string]string{"Tw-Actor": actor()}); err != nil {
		return err
	}
	res := map[string]any{"workload": pos[0], "deploymentID": d.DeploymentID, "result": kr, "timingsMs": map[string]float64{"total": ms(time.Since(t0))}}
	emit(f, res, func() {
		fmt.Printf("✓ undeployed %s (%s) · %d objects killed · lease released\n", tsafe(pos[0]), tsafe(d.DeploymentID), len(kr.Killed))
	})
	return nil
}
