//go:build e2ereal

package tssvc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/tailscale/hujson"
)

// capsPolicy is what TestRealServiceCaps needs in the test tailnet's
// policy (see the package comment), less the Service auto-approver, which
// policy.Snippet doesn't carry (applyCapsAutoApprover adds it).
func capsPolicy() policy.Snippet {
	return policy.Snippet{
		TagOwners: map[string][]string{capsHostTag: {"autogroup:admin"}},
		Grants: []map[string]any{
			{"src": []string{"tag:ttest"}, "dst": []string{capsSvc}, "ip": []string{"tcp:80"},
				"app": map[string]any{capViaSvc: []any{map[string]any{"via": "svc"}}}},
			{"src": []string{"tag:ttest"}, "dst": []string{capsHostTag}, "ip": []string{"tcp:80"},
				"app": map[string]any{capViaNode: []any{map[string]any{"via": "node"}}}},
		},
	}
}

// applyCapsPolicy adds what the scenario needs to the test tailnet's policy,
// as the other real-tailnet suites do (e2e-oauth, e2e-mac, e2e-multi):
// additive only, validated, If-Match, a no-op when it's all there. The
// policy it replaced is saved next to the API key file.
func applyCapsPolicy(t *testing.T, ctx context.Context, api *tsapi.Client, keyFile, run string) {
	t.Helper()
	backup := func(tag string, b []byte) string {
		p := filepath.Join(filepath.Dir(keyFile), strings.TrimSuffix(filepath.Base(keyFile), "_api.env")+"_acl.pre-h22-"+tag+"-"+run+".hujson")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Logf("policy backup %s: %v", p, err)
		}
		return p
	}
	ar, err := policy.ApplyRetry(ctx, api, capsPolicy(), policy.Options{AllowTags: []string{capsHostTag, "tag:ttest"}, AllowSrcs: []string{"tag:ttest"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if ar.Changed {
		t.Logf("policy: added %s (validated, If-Match %s; previous copy %s)", ar.Added.HuJSON(), ar.OldETag, backup("grants", ar.Before))
	}
	for i := 0; ; i++ {
		err := applyCapsAutoApprover(ctx, api, backup)
		if err == nil {
			return
		}
		if i < 2 && policy.IsPreconditionFailed(err) {
			time.Sleep(time.Second)
			continue
		}
		t.Fatalf("policy autoApprovers: %v", err)
	}
}

// applyCapsAutoApprover adds autoApprovers.services[capsSvc] = [capsHostTag]
// (the only change it may make: checked before writing), so the host's
// advertisement of the Service is approved without an admin.
func applyCapsAutoApprover(ctx context.Context, api *tsapi.Client, backup func(string, []byte) string) error {
	cur, etag, err := api.GetPolicyHuJSON(ctx)
	if err != nil {
		return fmt.Errorf("read policy: %w", err)
	}
	var have struct {
		AutoApprovers struct {
			Services map[string][]string `json:"services"`
		} `json:"autoApprovers"`
	}
	if err := unmarshalHuJSON(cur, &have); err != nil {
		return err
	}
	if slices.Contains(have.AutoApprovers.Services[capsSvc], capsHostTag) {
		return nil
	}
	v, err := hujson.Parse(cur)
	if err != nil {
		return err
	}
	esc := func(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }
	var op map[string]any
	switch {
	case v.Find("/autoApprovers") == nil:
		op = map[string]any{"op": "add", "path": "/autoApprovers", "value": map[string]any{"services": map[string]any{capsSvc: []string{capsHostTag}}}}
	case v.Find("/autoApprovers/services") == nil:
		op = map[string]any{"op": "add", "path": "/autoApprovers/services", "value": map[string]any{capsSvc: []string{capsHostTag}}}
	case v.Find("/autoApprovers/services/"+esc(capsSvc)) == nil:
		op = map[string]any{"op": "add", "path": "/autoApprovers/services/" + esc(capsSvc), "value": []string{capsHostTag}}
	default:
		op = map[string]any{"op": "add", "path": "/autoApprovers/services/" + esc(capsSvc) + "/-", "value": capsHostTag}
	}
	pb, _ := json.Marshal([]any{op})
	if err := v.Patch(pb); err != nil {
		return fmt.Errorf("patch: %w", err)
	}
	v.Format()
	next := v.Pack()

	// Additive check: with the one approver taken out again, the policy is
	// exactly what it was.
	var before, after map[string]any
	if err := unmarshalHuJSON(cur, &before); err != nil {
		return err
	}
	if err := unmarshalHuJSON(next, &after); err != nil {
		return err
	}
	aa, _ := after["autoApprovers"].(map[string]any)
	svcs, _ := aa["services"].(map[string]any)
	list, _ := svcs[capsSvc].([]any)
	list = slices.DeleteFunc(list, func(x any) bool { return x == capsHostTag })
	if len(list) == 0 {
		delete(svcs, capsSvc)
	} else {
		svcs[capsSvc] = list
	}
	if len(svcs) == 0 {
		delete(aa, "services")
	}
	if len(aa) == 0 {
		delete(after, "autoApprovers")
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("refusing to write: the patch changed more than autoApprovers.services[%s]", capsSvc)
	}
	if err := api.ValidatePolicy(ctx, next); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	if _, err := api.SetPolicy(ctx, next, etag); err != nil {
		return err
	}
	fmt.Printf("policy: added autoApprovers.services[%s] = [%s] (If-Match %s; previous copy %s)\n", capsSvc, capsHostTag, etag, backup("approver", cur))
	return nil
}

func unmarshalHuJSON(b []byte, v any) error {
	std, err := hujson.Standardize(b)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	return json.Unmarshal(std, v)
}
