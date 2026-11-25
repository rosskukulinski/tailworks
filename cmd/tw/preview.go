package main

// Preview deploys (ADR-048). `tw deploy --preview` (app.go)
// runs a bundle as the workload `<workload>--preview-<alias>` next to the
// workload, which it doesn't touch. `tw previews <workload>` lists them
// (GET /v1/tenants/{id}/workloads/{workload}/previews); `tw promote
// <workload> <alias>` deploys a preview's bundle to the workload, through
// the same deploy path as `tw deploy`, then undeploys the preview (POST
// …/promotions). Like rollback, the CLI preflights the bundle against the
// current policy first and waits until the workload is ready.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// previewEntry is one item of GET …/previews.
type previewEntry struct {
	Workload     string              `json:"workload"`
	Alias        string              `json:"alias"`
	PreviewOf    string              `json:"previewOf"`
	BundleSHA256 string              `json:"bundleSHA256,omitempty"`
	Deployment   mgmt.DeploymentView `json:"deployment"`
}

type previewsResult struct {
	Workload string         `json:"workload"`
	Tenant   string         `json:"tenant"`
	TenantID string         `json:"tenantID"`
	Previews []previewEntry `json:"previews"`
}

func cmdPreviews(args []string) error {
	f := newFlags("previews")
	tenantF := tenantFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fail("bad_request", "tw previews needs one workload", "usage: tw "+usageOf("previews"))
	}
	workload := pos[0]
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
	var pg struct {
		Items []previewEntry `json:"items"`
	}
	if err := s.mgmt().Do(ctx, "GET", "/v1/tenants/"+url.PathEscape(tn.TenantID)+"/workloads/"+url.PathEscape(workload)+"/previews", nil, &pg); err != nil {
		return err
	}
	res := previewsResult{Workload: workload, Tenant: tn.Tailnet, TenantID: tn.TenantID, Previews: pg.Items}
	if res.Previews == nil {
		res.Previews = []previewEntry{}
	}
	emit(f, res, func() {
		if len(res.Previews) == 0 {
			fmt.Printf("%s has no running previews · tw deploy --preview to start one\n", workload)
			return
		}
		fmt.Printf("%-20s %-19s %-12s %-12s %-22s %s\n", "ALIAS", "CREATED", "BUNDLE", "STATUS", "HOST", "URL")
		for _, p := range res.Previews {
			d := p.Deployment
			fmt.Printf("%-20s %-19s %-12s %-12s %-22s http://%s/\n", tsafe(p.Alias), time.UnixMilli(d.CreatedAt).Local().Format("2006-01-02 15:04:05"),
				tsafe(short12(p.BundleSHA256)), tsafe(d.Status), tsafe(d.Host), tsafe(d.Endpoint.MagicDNS))
		}
		fmt.Printf("\ntw promote %s <alias> · tw undeploy %s--preview-<alias>\n", workload, workload)
	})
	return nil
}

type promoteResult struct {
	Workload       string                     `json:"workload"`
	Tenant         string                     `json:"tenant"`
	TenantID       string                     `json:"tenantID"`
	Preview        string                     `json:"preview"`
	PromotedFrom   string                     `json:"promotedFrom"`
	PreviewStopped bool                       `json:"previewStopped"`
	DeploymentID   string                     `json:"deploymentID"`
	Host           string                     `json:"host"`
	Status         string                     `json:"status"`
	Preflight      *mgmt.PreflightResult      `json:"preflight,omitempty"`
	Endpoint       any                        `json:"endpoint,omitempty"`
	Placement      *mgmt.PlacementDecision    `json:"placement,omitempty"`
	Public         *mgmt.DeploymentPublicView `json:"public,omitempty"`
	Timings        map[string]float64         `json:"timingsMs"`
}

func cmdPromote(args []string) error {
	f := newFlags("promote")
	tenantF := tenantFlag(f)
	po := addPlacementFlags(f, "promote") // placementflags.go
	allowMissing, noWait, wait := po.allowMissing, po.noWait, po.wait
	keep := f.Bool("keep-preview", false, "leave the preview running (default: undeploy it once promoted)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return fail("bad_request", "tw promote needs a workload and a preview (its alias; tw previews <workload> lists them)", "usage: tw "+usageOf("promote"))
	}
	workload, preview := pos[0], pos[1]
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
	path := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/promotions"
	body := map[string]any{"preview": preview}
	po.apply(body)
	if *keep {
		body["keepPreview"] = true
	}
	t0 := time.Now()
	res := promoteResult{Workload: workload, Tenant: tn.Tailnet, TenantID: tn.TenantID, Timings: map[string]float64{}}
	ev := newEvents(*f.json, os.Stderr)
	ev.emit("start", map[string]any{"workload": workload, "tenant": tn.Tailnet, "preview": preview})

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
	var pr struct {
		Preview        string              `json:"preview"`
		PromotedFrom   string              `json:"promotedFrom"`
		PreviewStopped bool                `json:"previewStopped"`
		Deployment     mgmt.DeploymentView `json:"deployment"`
	}
	if err := mc.DoHeaders(ctx, "POST", path, body, &pr, hdr); err != nil {
		return err
	}
	dv := pr.Deployment
	res.Preview, res.PromotedFrom, res.PreviewStopped = pr.Preview, pr.PromotedFrom, pr.PreviewStopped
	res.DeploymentID, res.Host, res.Placement = dv.DeploymentID, dv.Host, dv.Placement
	res.Timings["promote_api"] = ms(time.Since(ds))
	ev.emit("placed", map[string]any{"deploymentID": dv.DeploymentID, "host": dv.Host, "placement": dv.Placement, "promotedFrom": pr.PromotedFrom})
	if !*f.json {
		fmt.Printf("✓ promote    %s → the bundle of preview %s (%s)\n", workload, pr.Preview, pr.PromotedFrom)
		if dv.Placement != nil {
			fmt.Printf("✓ placed     %s\n", tsafe(dv.Placement.Summary))
		}
		if pr.PreviewStopped {
			fmt.Printf("✓ undeployed %s\n", pr.Preview)
		}
	}
	if *noWait {
		res.Status = "assigned"
		res.Timings["total"] = ms(time.Since(t0))
		emit(f, res, func() {
			fmt.Printf("✓ promoting %s@%s (%s) · `tw deployments %s` to follow\n", workload, dv.Host, dv.DeploymentID, workload)
		})
		return nil
	}
	v, _, err := readyWait{mc: mc, ev: ev, id: dv.DeploymentID, workload: workload, what: "the promoted " + workload, since: ds, wait: *wait}.run(ctx) // deployready.go
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
		fmt.Printf("✓ promoted   %s@%s · %s · http://%s/  (%.1fs)\n", workload, v.Host, dv.DeploymentID, v.Endpoint.MagicDNS, res.Timings["ready"]/1000)
		fmt.Printf("  code and config only: the workload keeps its own Durable Object storage, D1, KV, R2 and secrets; the preview's data is not copied\n")
	})
	return nil
}

func short12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "-"
	}
	return sha
}

// addPreview asks for a preview deploy (--preview, --preview-alias) and
// returns the preview's workload name ("" for a plain deploy). mgmt's
// default alias is the start of the bundle's SHA-256, as here.
func addPreview(body map[string]any, workload string, preview bool, alias string, bundle []byte) string {
	if !preview && alias == "" {
		return ""
	}
	body["preview"] = true
	if alias != "" {
		body["previewAlias"] = alias
	} else {
		sum := sha256.Sum256(bundle)
		alias = hex.EncodeToString(sum[:4])
	}
	return workload + "--preview-" + alias
}

func orName(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// previewNotice is what `tw deploy --preview` prints once the preview is
// ready.
func previewNotice(workload, preview, url string) string {
	alias := strings.TrimPrefix(preview, workload+"--preview-")
	return fmt.Sprintf("✓ preview    %s of %s · %s · its own Durable Object storage and stores, %s's secrets; %s itself is unchanged\n  tw promote %s %s to make it %s · tw undeploy %s to discard it\n",
		alias, workload, url, workload, workload, workload, alias, workload, preview)
}
