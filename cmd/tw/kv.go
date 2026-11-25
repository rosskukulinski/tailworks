package main

// Workers KV (ADR-037), CLI side, like `wrangler kv`:
//
//	tw kv namespace create|ls|rm [NAME]
//	tw kv key put <KEY> [VALUE] | get <KEY> | rm <KEY> | ls [--prefix P]
//
// (wrangler's list and delete work too: normalizeVerb.) A namespace
// belongs to a workload (default: the app in the current directory). Key
// commands name it with --namespace: its name, or its binding in the
// app's wrangler config kv_namespaces, as a deploy would resolve it
// (storename.go); without it, the app's only kv_namespaces entry.
// Everything goes to mgmt's /v1 API: the namespace is a platform
// store of kind "kv", and key writes are journal appends a running
// workload sees on its next sync (within 60 s).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/kvwire"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// kvInput is where `tw kv key put` reads a value given neither as an
// argument nor with --path (tests replace it).
var kvInput = func() io.Reader { return os.Stdin }

// kvOutput is where `tw kv key get` writes the raw value (tests replace it).
var kvOutput = func() io.Writer { return os.Stdout }

type kvResult struct {
	Workload   string            `json:"workload"`
	TenantID   string            `json:"tenantID"`
	Namespace  string            `json:"namespace,omitempty"`
	Namespaces *[]mgmt.StoreInfo `json:"namespaces,omitempty"` // namespace list: always present
	Created    *bool             `json:"created,omitempty"`
	Deleted    string            `json:"deleted,omitempty"`
	Key        *mgmt.KVKey       `json:"key,omitempty"`
	Keys       *[]mgmt.KVKey     `json:"keys,omitempty"` // key list: always present
	Value      *mgmt.KVValue     `json:"value,omitempty"`
}

// kvUsageLine is tw kv's usage line (the commands table's).
const kvUsageLine = "kv namespace create <NAME> | kv namespace ls | kv namespace rm <NAME> [--yes] | kv key put <KEY> [VALUE] [--namespace N] [--path FILE] [--ttl S] [--expiration T] [--metadata JSON] | kv key get <KEY> [--namespace N] | kv key rm <KEY> [--namespace N] | kv key ls [--namespace N] [--prefix P]   [--workload <workload>] [--tenant <tailnet>]"

func kvUsage() string { return "usage: tw " + usageOf("kv") }

// kvVerbs are tw kv's commands and how many arguments each takes (key
// put: one or two, checked in cmdKV).
var kvVerbs = map[string]int{
	"key get":          1,
	"key ls":           0,
	"key put":          1,
	"key rm":           1,
	"namespace create": 1,
	"namespace ls":     0,
	"namespace rm":     1,
}

// kvOpts are tw kv's flags. A verb registers only its own (kvFlags); the
// others keep their zero value.
type kvOpts struct {
	tenant, workload *string

	// key commands
	namespace, binding *string

	// key ls
	prefix *string

	// key put
	path, metadata  *string
	ttl, expiration *int64

	// namespace rm
	yes *bool
}

// kvFlags registers verb's flags ("key put"; anyVerb: every verb's) on f.
func kvFlags(f *flags, verb string) *kvOpts {
	o := &kvOpts{namespace: new(string), binding: new(string), prefix: new(string), path: new(string), metadata: new(string),
		ttl: new(int64), expiration: new(int64), yes: new(bool)}
	o.tenant = tenantFlag(f)
	o.workload = f.String("workload", "", "workload whose namespaces these are (default: the app in the current directory)")
	if onVerb(verb, "key put", "key get", "key rm", "key ls") {
		o.namespace = f.String("namespace", "", "the namespace: its name (a kv_namespaces id) or its binding in the app's wrangler config (default: the app's only kv_namespaces entry)")
		o.binding = f.String("binding", "", "same as --namespace (wrangler's spelling)")
	}
	if onVerb(verb, "key ls") {
		o.prefix = f.String("prefix", "", "only keys starting with this")
	}
	if onVerb(verb, "key put") {
		o.path = f.String("path", "", "read the value from this file")
		o.ttl = f.Int64("ttl", 0, "expire after this many seconds (≥ 60)")
		o.expiration = f.Int64("expiration", 0, "expire at this Unix time (≥ 60 s ahead)")
		o.metadata = f.String("metadata", "", "metadata (JSON, ≤ 1024 bytes)")
	}
	if onVerb(verb, "namespace rm") {
		o.yes = yesFlag(f)
	}
	return o
}

func cmdKV(args []string) error {
	verb, args, complete := scanVerb(args, func(f *flags) { kvFlags(f, anyVerb) }, func(string) int { return 2 })
	if _, ok := kvVerbs[verb]; complete && !ok {
		return fail("bad_request", fmt.Sprintf("unknown command %q", verb), kvUsage())
	}
	if !complete && verb != "" {
		return needsSubcommand("kv")
	}
	f := newFlags("kv")
	o := kvFlags(f, verb)
	rest, err := f.parse(args)
	if err != nil {
		return err
	}
	if !complete {
		return needsSubcommand("kv")
	}
	group, _, _ := strings.Cut(verb, " ")
	switch n := kvVerbs[verb]; {
	case verb == "key put":
		if len(rest) < 1 || len(rest) > 2 || (len(rest) == 2 && *o.path != "") {
			return fail("bad_request", "tw kv key put needs the KEY and the value: an argument, --path FILE, or stdin", kvUsage())
		}
	case len(rest) == n:
	case n == 0:
		return fail("bad_request", "tw kv "+verb+" takes no arguments", kvUsage())
	case group == "key":
		return fail("bad_request", "tw kv "+verb+" needs the KEY", kvUsage())
	default:
		return fail("bad_request", "tw kv "+verb+" needs the namespace's NAME", kvUsage())
	}
	if verb == "namespace rm" {
		cmdline := "tw kv namespace rm " + rest[0]
		if *o.workload != "" {
			cmdline += " --workload " + *o.workload
		}
		if err := confirmDestroy(f, *o.yes, "deleting namespace "+rest[0]+" deletes every key in it", rest[0], cmdline); err != nil {
			return err
		}
	}

	// The workload (default: the app in the current directory), and for
	// key commands the namespace, which that app's kv_namespaces can name.
	workload := *o.workload
	var a *app
	if workload == "" || group == "key" {
		var appErr error
		if a, appErr = loadApp(".", ""); appErr != nil {
			a = nil
		}
		if workload == "" {
			if a == nil {
				extra := ""
				if group == "key" {
					extra = " --namespace <NAME>"
				}
				return appWorkloadErr(".", "kv "+verb, appErr, extra) // appworkload.go
			}
			workload = a.Name
		}
	}
	if workloadLabel(workload) != workload {
		return fail("bad_request", fmt.Sprintf("workload %q is not a DNS label", workload))
	}
	ns := ""
	switch {
	case group == "namespace" && len(rest) == 1:
		ns = rest[0]
	case group == "key":
		if ns, err = kvNamespaceOf(a, workload, *o.namespace, *o.binding); err != nil {
			return err
		}
	}
	if ns != "" && !mgmt.ValidStoreName(ns) {
		return fail("bad_request", fmt.Sprintf("namespace name %q must be 1-64 of [A-Za-z0-9_.-], starting with a letter or digit", ns))
	}
	key := ""
	if group == "key" && len(rest) > 0 {
		key = rest[0]
		if err := kvwire.CheckKey(key); err != nil {
			return fail("bad_request", "key: "+err.Error())
		}
	}
	var put mgmt.KVPut
	if verb == "key put" {
		if put, err = kvPutBody(rest, *o.path, *o.ttl, *o.expiration, *o.metadata); err != nil {
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
	stores := "/v1/tenants/" + url.PathEscape(tn.TenantID) + "/workloads/" + url.PathEscape(workload) + "/stores"
	nsPath := stores + "/kv/" + url.PathEscape(ns)
	valPath := nsPath + "/value?key=" + url.QueryEscape(key)
	res := kvResult{Workload: workload, TenantID: tn.TenantID, Namespace: ns}
	mc := s.mgmt()
	hdr := map[string]string{"Tw-Actor": actor()}

	switch verb {
	case "namespace create":
		var st mgmt.StoreInfo
		created := mc.Do(ctx, "GET", nsPath, nil, &st) != nil
		if created {
			if err := mc.DoHeaders(ctx, "PUT", nsPath, nil, &st, hdr); err != nil {
				return err
			}
		}
		res.Created = &created
		emit(f, res, func() {
			verb := "created"
			if !created {
				verb = "already exists"
			}
			fmt.Printf("✓ KV namespace %s %s for workload %s (tenant %s)\n", ns, verb, workload, tn.Tailnet)
			fmt.Printf("  bind it in wrangler.jsonc: \"kv_namespaces\": [{ \"binding\": \"MY_KV\", \"id\": %q }]\n", ns)
		})
	case "namespace ls":
		items, err := listAll[mgmt.StoreInfo](ctx, mc, stores+"?kind=kv", 0)
		if err != nil {
			return err
		}
		nss := append([]mgmt.StoreInfo{}, items...)
		res.Namespaces = &nss
		emit(f, res, func() {
			if len(items) == 0 {
				fmt.Printf("no KV namespaces for workload %s (tenant %s) · create one: tw kv namespace create <NAME>, or deploy an app with kv_namespaces\n", workload, tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %10s %12s  %s\n", "NAME", "ENTRIES", "BYTES", "UPDATED")
			for _, st := range items {
				fmt.Printf("%-32s %10d %12d  %s\n", tsafe(st.Name), st.Entries, st.Bytes, time.UnixMilli(st.UpdatedAt).Local().Format("2006-01-02 15:04:05"))
				printStoreDegraded(st.Degraded, storeFix(st))
			}
		})
	case "namespace rm":
		if err := mc.DoHeaders(ctx, "DELETE", nsPath, nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = ns
		emit(f, res, func() {
			fmt.Printf("✓ KV namespace %s of workload %s deleted, with its data (tenant %s)\n", ns, workload, tn.Tailnet)
			fmt.Printf("  a running workload empties its copy on its next sync; its next write starts a new, empty namespace\n")
		})
	case "key put":
		var k mgmt.KVKey
		if err := mc.DoHeaders(ctx, "PUT", valPath, put, &k, hdr); err != nil {
			return err
		}
		res.Key = &k
		emit(f, res, func() {
			fmt.Printf("✓ wrote %q to KV namespace %s of workload %s (%d bytes)\n", key, ns, workload, len(put.Value))
			fmt.Printf("  running workloads see it on their next sync (within 60 s)\n")
		})
	case "key get":
		var v mgmt.KVValue
		if err := mc.Do(ctx, "GET", valPath, nil, &v); err != nil {
			return err
		}
		res.Value = &v
		if *f.json {
			emit(f, res, nil)
			return nil
		}
		_, err := kvOutput().Write(v.Value)
		return err
	case "key rm":
		if err := mc.DoHeaders(ctx, "DELETE", valPath, nil, nil, hdr); err != nil {
			return err
		}
		res.Deleted = key
		emit(f, res, func() { fmt.Printf("✓ deleted %q from KV namespace %s of workload %s\n", key, ns, workload) })
	case "key ls":
		keys := []mgmt.KVKey{}
		res.Keys = &keys
		cursor := ""
		for {
			var p mgmt.KVKeyPage
			q := url.Values{"prefix": {*o.prefix}, "cursor": {cursor}}
			if err := mc.Do(ctx, "GET", nsPath+"/keys?"+q.Encode(), nil, &p); err != nil {
				return err
			}
			keys = append(keys, p.Keys...)
			if p.ListComplete {
				break
			}
			cursor = p.Cursor
		}
		emit(f, res, func() {
			if len(keys) == 0 {
				fmt.Printf("no keys in KV namespace %s of workload %s\n", ns, workload)
				return
			}
			fmt.Printf("%-40s %-20s %s\n", "KEY", "EXPIRES", "METADATA")
			for _, k := range keys {
				exp := "-"
				if k.Expiration != 0 {
					exp = time.Unix(k.Expiration, 0).Local().Format("2006-01-02 15:04:05")
				}
				meta := "-"
				if len(k.Metadata) > 0 {
					meta = tsafe(string(k.Metadata))
				}
				fmt.Printf("%-40s %-20s %s\n", strconv.Quote(k.Name), exp, meta)
			}
		})
	}
	return nil
}

// kvNamespaceOf resolves a key command's namespace (issue #458):
// --namespace (or --binding, the same), a name or a binding of the app in
// the current directory (storeName); without either, the app's only
// kv_namespaces entry, when the workload is that app's. a is that app (nil
// without one).
func kvNamespaceOf(a *app, workload, ns, binding string) (string, error) {
	switch {
	case ns != "" && binding != "" && ns != binding:
		return "", fail("bad_request", "--namespace and --binding name different namespaces: give one", kvUsage())
	case ns == "":
		ns = binding
	}
	bs, err := appStores(a, "kv")
	if err != nil {
		return "", err
	}
	if ns != "" {
		return storeName(bs, ns), nil
	}
	// No namespace named: the app's, and only for the app's own workload,
	// so a key rm --workload <other> never picks this app's namespace.
	if a == nil || workload != a.Name {
		return "", fail("bad_request", "key commands for workload "+workload+" need the namespace: --namespace <NAME>", kvUsage())
	}
	switch len(bs) {
	case 1:
		return bs[0].Name, nil
	case 0:
		return "", fail("bad_request", "the app has no kv_namespaces: pass --namespace <NAME>", kvUsage())
	}
	return "", fail("bad_request", fmt.Sprintf("the app has %d kv_namespaces (%s): pass --namespace <NAME|BINDING>", len(bs), bindingList(bs)), kvUsage())
}

func kvPutBody(rest []string, path string, ttl, exp int64, meta string) (mgmt.KVPut, error) {
	var p mgmt.KVPut
	var err error
	switch {
	case len(rest) == 2:
		p.Value = []byte(rest[1])
	case path != "":
		if p.Value, err = os.ReadFile(path); err != nil {
			return p, fail("bad_request", "--path: "+err.Error())
		}
	default:
		if p.Value, err = io.ReadAll(io.LimitReader(kvInput(), kvwire.MaxValueBytes+1)); err != nil {
			return p, fail("bad_request", "reading the value from stdin: "+err.Error())
		}
	}
	if len(p.Value) > kvwire.MaxValueBytes {
		return p, fail("bad_request", fmt.Sprintf("the value is over %d bytes (KV's limit on tailworks)", kvwire.MaxValueBytes))
	}
	p.ExpirationTTL, p.Expiration = ttl, exp
	if meta != "" {
		if !json.Valid([]byte(meta)) {
			return p, fail("bad_request", "--metadata must be JSON")
		}
		var b bytes.Buffer
		json.Compact(&b, []byte(meta))
		p.Metadata = b.Bytes()
	}
	return p, nil
}
