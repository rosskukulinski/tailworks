package bundle

import (
	"encoding/json"
	"fmt"
)

// R2 buckets (ADR-036): wrangler.jsonc "r2_buckets" entries are
// platform stores of kind "r2", named by their bucket_name (the binding
// name when absent). preview_bucket_name and jurisdiction are accepted and
// ignored: a bucket is the workload's, on the host's R2 backend.
func init() {
	RegisterStoreKind(StoreKind{Kind: "r2", WranglerKey: "r2_buckets", Parse: parseR2Buckets})
}

func parseR2Buckets(raw json.RawMessage) ([]StoreBinding, error) {
	var in []struct {
		Binding      string `json:"binding"`
		BucketName   string `json:"bucket_name"`
		Preview      string `json:"preview_bucket_name"`
		Jurisdiction string `json:"jurisdiction"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("want [{\"binding\": …, \"bucket_name\": …}]: %w", err)
	}
	out := make([]StoreBinding, 0, len(in))
	seen := map[string]string{}
	for _, b := range in {
		name := b.BucketName
		if name == "" {
			name = b.Binding
		}
		if o, dup := seen[name]; dup {
			return nil, fmt.Errorf("bindings %s and %s name the same bucket %q", o, b.Binding, name)
		}
		seen[name] = b.Binding
		out = append(out, StoreBinding{Binding: b.Binding, Name: name})
	}
	return out, nil
}
