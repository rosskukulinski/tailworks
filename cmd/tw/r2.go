package main

// R2 buckets (ADR-036), CLI side: `tw r2 bucket ls|create|rm` and
// `tw r2 object ls`. A bucket is a platform store of kind "r2", so these
// use mgmt's stores API (ADR-044); object ls is mgmt's
// GET …/stores/r2/{name}/objects, which folds the bucket's index. Object
// bytes live in the hosts' R2 backend, which mgmt can't reach: object
// get/put from the CLI is not built yet.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// r2Bucket is a bucket as the stores API describes it.
type r2Bucket struct {
	Name      string `json:"name"`
	Head      int64  `json:"head"`
	Entries   int64  `json:"entries"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`

	// Degraded (#147): the bucket's index stopped its replay at an entry.
	Degraded *mgmt.StoreDegraded `json:"degraded,omitempty"`
}

// r2Object is one live key of a bucket's index.
type r2Object struct {
	Key         string `json:"key"`
	Size        int64  `json:"size"`
	ETag        string `json:"etag"`
	Uploaded    int64  `json:"uploaded"`
	ContentType string `json:"contentType,omitempty"`
}

type r2Result struct {
	Workload string     `json:"workload"`
	TenantID string     `json:"tenantID"`
	Bucket   string     `json:"bucket,omitempty"`
	Buckets  []r2Bucket `json:"buckets,omitempty"`
	Objects  []r2Object `json:"objects,omitempty"`
	Created  bool       `json:"created,omitempty"`
	Deleted  bool       `json:"deleted,omitempty"`
}

const r2Usage = "r2 bucket ls | r2 bucket create <bucket> | r2 bucket rm <bucket> | r2 object ls <bucket> [--prefix P]   [--workload <workload>] [--tenant <tailnet>]"

// r2Opts are tw r2's flags. A verb registers only its own (r2Flags); the
// others keep their zero value.
type r2Opts struct {
	tenant, workload *string

	// object ls
	prefix *string

	// bucket rm
	yes *bool
}

// r2Flags registers verb's flags ("object ls"; anyVerb: every verb's) on f.
func r2Flags(f *flags, verb string) *r2Opts {
	o := &r2Opts{prefix: new(string), yes: new(bool)}
	o.tenant = tenantFlag(f)
	o.workload = f.String("workload", "", "workload (default: the app in the current directory, as tw deploy names it)")
	if onVerb(verb, "object ls") {
		o.prefix = f.String("prefix", "", "only keys starting with this")
	}
	if onVerb(verb, "bucket rm") {
		o.yes = yesFlag(f)
	}
	return o
}

func cmdR2(args []string) error {
	verb, args, complete := scanVerb(args, func(f *flags) { r2Flags(f, anyVerb) }, func(string) int { return 2 })
	what, sub, _ := strings.Cut(verb, " ")
	switch {
	case verb == "":
	case verb == "object get" || verb == "object put":
		return fail("not_supported", "tw r2 object "+sub+" is not built yet: object bytes live in the hosts' R2 backend, which mgmt can't reach",
			"read or write objects through the workload's binding (env.<BUCKET>) for now")
	case !complete || !onVerb(verb, "bucket ls", "bucket create", "bucket rm", "object ls"):
		return fail("bad_request", fmt.Sprintf("unknown or incomplete command: tw r2 %s %s", what, sub), "usage: tw "+r2Usage)
	}
	f := newFlags("r2")
	o := r2Flags(f, verb)
	p, err := f.parse(args)
	if err != nil {
		return err
	}
	if verb == "" {
		return needsSubcommand("r2")
	}
	want := 1
	if verb == "bucket ls" {
		want = 0
	}
	if len(p) != want {
		return fail("bad_request", fmt.Sprintf("unknown or incomplete command: tw r2 %s %s", what, sub), "usage: tw "+r2Usage)
	}
	// The app in the current directory names the workload, and its
	// r2_buckets can name a bucket by its binding (storeName).
	a, appErr := loadApp(".", "")
	if appErr != nil {
		a = nil
	}
	bucket := ""
	if len(p) == 1 {
		bs, err := appStores(a, "r2")
		if err != nil {
			return err
		}
		bucket = storeName(bs, p[0])
		if !bundle.StoreNameRE.MatchString(bucket) {
			return fail("bad_request", fmt.Sprintf("bucket name %q must match %s", bucket, bundle.StoreNameRE))
		}
	}
	workload := *o.workload
	if workload == "" {
		if a == nil {
			return appWorkloadErr(".", "r2 "+verb, appErr, "") // appworkload.go
		}
		workload = a.Name
	}
	if workloadLabel(workload) != workload {
		return fail("bad_request", fmt.Sprintf("workload %q is not a DNS label", workload))
	}
	if verb == "bucket rm" {
		if err := confirmDestroy(f, *o.yes, "deleting bucket "+bucket+" of workload "+workload+" deletes every object in it", bucket,
			"tw r2 bucket rm "+bucket+" --workload "+workload); err != nil {
			return err
		}
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(*o.tenant)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/stores"
	res := r2Result{Workload: workload, TenantID: tn.TenantID, Bucket: bucket}
	hdr := map[string]string{"Tw-Actor": actor()}
	switch verb {
	case "bucket ls":
		items, err := listAll[r2Bucket](ctx, s.mgmt(), base+"?kind=r2", 0)
		if err != nil {
			return err
		}
		res.Buckets = items
		if res.Buckets == nil {
			res.Buckets = []r2Bucket{}
		}
		emit(f, res, func() {
			if len(items) == 0 {
				fmt.Printf("no R2 buckets for workload %s (tenant %s) · a bucket appears on its first write, or: tw r2 bucket create <bucket>\n", workload, tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %10s %-20s\n", "BUCKET", "INDEX", "UPDATED")
			for _, b := range items {
				fmt.Printf("%-32s %10s %-20s\n", tsafe(b.Name), humanBytes(b.Bytes), time.UnixMilli(b.UpdatedAt).Local().Format("2006-01-02 15:04:05"))
				printStoreDegraded(b.Degraded, storeFix(mgmt.StoreInfo{Kind: "r2", Name: b.Name, Workload: workload, Degraded: b.Degraded}))
			}
		})
	case "bucket create":
		var st struct {
			CreatedAt int64 `json:"createdAt"`
			UpdatedAt int64 `json:"updatedAt"`
			Head      int64 `json:"head"`
		}
		if err := s.mgmt().DoHeaders(ctx, "PUT", base+"/r2/"+url.PathEscape(bucket), map[string]any{}, &st, hdr); err != nil {
			return err
		}
		res.Created = st.Head == 0 && st.CreatedAt == st.UpdatedAt
		emit(f, res, func() {
			fmt.Printf("✓ R2 bucket %s ready for workload %s (tenant %s)\n", bucket, workload, tn.Tailnet)
			fmt.Printf("  bind it in wrangler.jsonc: \"r2_buckets\": [{ \"binding\": \"BUCKET\", \"bucket_name\": %q }]\n", bucket)
		})
	case "bucket rm":
		if err := s.mgmt().DoHeaders(ctx, "DELETE", base+"/r2/"+url.PathEscape(bucket), nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = true
		emit(f, res, func() {
			fmt.Printf("✓ R2 bucket %s of workload %s deleted (tenant %s)\n", bucket, workload, tn.Tailnet)
			fmt.Printf("  its index is gone now; the workload drops its blobs the next time it uses the bucket\n")
		})
	case "object ls":
		objs, err := listAll[r2Object](ctx, s.mgmt(), base+"/r2/"+url.PathEscape(bucket)+"/objects?prefix="+url.QueryEscape(*o.prefix), 0)
		if err != nil {
			switch ce := asCLIErr(err); ce.Code {
			case "unknown_store": // a bucket appears on its first write
				objs = []r2Object{}
			case "not_found": // a tw-mgmt without GET …/r2/{name}/objects
				ce.Fix = append(ce.Fix, "upgrade tw-mgmt: listing R2 objects needs this release's mgmt (tw upgrade)")
				return ce
			default:
				return ce
			}
		}
		res.Objects = objs
		emit(f, res, func() {
			if len(objs) == 0 {
				fmt.Printf("no objects in R2 bucket %s of workload %s\n", bucket, workload)
				return
			}
			fmt.Printf("%-48s %10s %-20s %s\n", "KEY", "SIZE", "UPLOADED", "ETAG")
			for _, o := range objs {
				fmt.Printf("%-48s %10s %-20s %s\n", tsafe(o.Key), humanBytes(o.Size), time.UnixMilli(o.Uploaded).Local().Format("2006-01-02 15:04:05"), tsafe(o.ETag))
			}
		})
	}
	return nil
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	d, e := int64(u), 0
	for m := n / u; m >= u; m /= u {
		d *= u
		e++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(d), "KMGTPE"[e])
}
