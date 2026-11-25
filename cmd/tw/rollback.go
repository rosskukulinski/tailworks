package main

// `tw rollback <workload> [<deployment>]` (ADR-046): redeploy
// an earlier deployment's stored bundle as a new deployment (POST
// /v1/tenants/{id}/workloads/{workload}/rollbacks). mgmt runs it through
// the same deploy path as `tw deploy` (placement, lease epoch+1, host
// fencing); like deploy, the CLI preflights the bundle against the current
// policy first and waits until the workload is ready.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

type rollbackResult struct {
	Workload     string                     `json:"workload"`
	Tenant       string                     `json:"tenant"`
	TenantID     string                     `json:"tenantID"`
	RollbackOf   string                     `json:"rollbackOf"`
	Target       mgmt.DeploymentRecord      `json:"target"`
	DeploymentID string                     `json:"deploymentID"`
	Host         string                     `json:"host"`
	Status       string                     `json:"status"`
	Preflight    *mgmt.PreflightResult      `json:"preflight,omitempty"`
	Endpoint     any                        `json:"endpoint,omitempty"`
	Placement    *mgmt.PlacementDecision    `json:"placement,omitempty"`
	Public       *mgmt.DeploymentPublicView `json:"public,omitempty"`
	Timings      map[string]float64         `json:"timingsMs"`
}

func cmdRollback(args []string) error {
	f := newFlags("rollback")
	tenantF := tenantFlag(f)
	po := addPlacementFlags(f, "roll back") // placementflags.go
	allowMissing, noWait, wait := po.allowMissing, po.noWait, po.wait
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return fail("bad_request", "tw rollback needs a workload (and optionally a deployment ID)", "usage: tw "+usageOf("rollback"))
	}
	workload, target := pos[0], ""
	if len(pos) == 2 {
		target = pos[1]
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
	ctx, cancel := context.WithTimeout(context.Background(), *wait+time.Minute)
	defer cancel()
	mc := s.mgmt()
	hdr := map[string]string{"Tw-Actor": actor()}
	path := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/rollbacks"
	body := map[string]any{}
	if target != "" {
		body["deploymentID"] = target
	}
	po.apply(body)
	t0 := time.Now()
	res := rollbackResult{Workload: workload, Tenant: tn.Tailnet, TenantID: tn.TenantID, Timings: map[string]float64{}}
	ev := newEvents(*f.json, os.Stderr)
	ev.emit("start", map[string]any{"workload": workload, "tenant": tn.Tailnet, "target": target})

	// Preflight the stored bundle against today's policy, as deploy does.
	var pf mgmt.PreflightResult
	if err := mc.DoHeaders(ctx, "POST", path+"?dryRun=true", body, &pf, hdr); err != nil {
		me, ok := mgmtapi.AsError(err)
		if !ok || me.Code != "policy_unavailable" {
			return err
		}
		pf = mgmt.PreflightResult{OK: true}
		if !*f.json {
			fmt.Printf("› preflight  skipped: mgmt can't read the tailnet policy (%s)\n", tsafe(firstLine(me.Message)))
		}
	}
	res.Preflight = &pf
	res.Timings["preflight"] = ms(time.Since(t0))
	ev.emit("preflight", map[string]any{"ok": pf.OK, "preflight": pf})
	if !pf.OK && !*allowMissing {
		return preflightFailure(pf)
	}

	ds := time.Now()
	var rb struct {
		RollbackOf string                `json:"rollbackOf"`
		Target     mgmt.DeploymentRecord `json:"target"`
		Deployment mgmt.DeploymentView   `json:"deployment"`
	}
	if err := mc.DoHeaders(ctx, "POST", path, body, &rb, hdr); err != nil {
		return err
	}
	dv := rb.Deployment
	res.RollbackOf, res.Target, res.DeploymentID, res.Host, res.Placement = rb.RollbackOf, rb.Target, dv.DeploymentID, dv.Host, dv.Placement
	res.Timings["rollback_api"] = ms(time.Since(ds))
	ev.emit("placed", map[string]any{"deploymentID": dv.DeploymentID, "host": dv.Host, "placement": dv.Placement, "rollbackOf": rb.RollbackOf})
	if !*f.json {
		sha := rb.Target.BundleSHA256
		if len(sha) > 12 {
			sha = sha[:12]
		}
		fmt.Printf("✓ rollback   %s → the bundle of %s (%s, deployed %s by %s)\n", workload, rb.RollbackOf, sha,
			time.UnixMilli(rb.Target.CreatedAt).Local().Format("2006-01-02 15:04:05"), orDash(rb.Target.DeployedBy))
		if dv.Placement != nil {
			fmt.Printf("✓ placed     %s\n", tsafe(dv.Placement.Summary))
		}
	}
	if *noWait {
		res.Status = "assigned"
		res.Timings["total"] = ms(time.Since(t0))
		emit(f, res, func() {
			fmt.Printf("✓ rolling back %s@%s (%s) · `tw deployments %s` to follow\n", workload, dv.Host, dv.DeploymentID, workload)
		})
		return nil
	}
	v, _, err := readyWait{mc: mc, ev: ev, id: dv.DeploymentID, workload: workload, what: "the rolled-back " + workload, since: ds, wait: *wait}.run(ctx) // deployready.go
	if err != nil {
		return err
	}
	if err := waitReplicas(ctx, mc, mgmt.DeployResponse{DeploymentID: dv.DeploymentID, Host: dv.Host, Replicas: dv.Replicas}, ds, *wait); err != nil {
		return err
	}
	res.Status, res.Endpoint, res.Public = v.Status, v.Endpoint, v.Public
	res.Timings["ready"] = ms(time.Since(ds))
	res.Timings["total"] = ms(time.Since(t0))
	ev.emit("ready", map[string]any{"deploymentID": dv.DeploymentID, "host": v.Host, "magicDNS": v.Endpoint.MagicDNS, "url": "http://" + v.Endpoint.MagicDNS + "/"})
	emit(f, res, func() {
		fmt.Printf("✓ rolled back %s@%s · %s · http://%s/  (%.1fs)\n", workload, v.Host, dv.DeploymentID, v.Endpoint.MagicDNS, res.Timings["ready"]/1000)
		fmt.Printf("  code and config only: Durable Object storage, D1 (migrations are forward-only), KV, R2 and queues keep their data; secrets are today's\n")
	})
	return nil
}
