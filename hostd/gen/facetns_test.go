package gen_test

import (
	"testing"
)

// TestFacetNamespaceIDs (#391): inside a facet, env.X is the platform's
// NsProxy stub, which only has getByName. The Agents SDK's getAgentByName
// does ns.get(ns.idFromName(name)), so idFromName must give an id that
// get() accepts: on the stub alone, idFromName returns an RpcPromise and
// get() fails with `Could not serialize object of type "RpcPromise"`.
// Every caller (the Worker, a facet object, a plain DO) reaches a facet
// object and a plain DO both ways.
func TestFacetNamespaceIDs(t *testing.T) {
	const js = `import { DurableObject } from "cloudflare:workers";
async function call(env, url) {
  const ns = env[url.searchParams.get("to")];
  const n = url.searchParams.get("n");
  const out = {};
  const id = ns.idFromName(n);
  out.idName = id.name;
  out.idEquals = id.equals(ns.idFromName(n)) && !id.equals(ns.idFromName(n + "-other"));
  out.viaGet = await ns.get(id).hello("get");
  out.viaGetByName = await ns.getByName(n).hello("getByName");
  try {
    ns.jurisdiction("eu");
  } catch (e) {
    out.jurisdiction = String(e.message); // workerd has none
  }
  return out;
}
class Base extends DurableObject {
  hello(how) { return how + ":" + this.ctx.id.name; }
  async fetch(req) { return Response.json(await call(this.env, new URL(req.url))); }
}
export class Facet extends Base {}
export class Plain extends Base {}
export class Target extends Base {}
export default { async fetch(req, env) { return Response.json(await call(env, new URL(req.url))); } };
`
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name": "facetns", "main": "index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [{"name": "FACET", "class_name": "Facet"}, {"name": "PLAIN", "class_name": "Plain"}, {"name": "TARGET", "class_name": "Target"}]}}`),
		"tailworks.jsonc": []byte(`{"tailnet": {"objects": {"Plain": {"egress": "workload"}}}}`),
		"index.js":        []byte(js),
	}
	in := startFiles(t, files)
	from := map[string]map[string]any{"Worker": wl, "Facet": obj("Facet", "caller-f"), "Plain": obj("Plain", "caller-p")}
	for w, tw := range from {
		for _, to := range []string{"TARGET", "PLAIN"} {
			n := "t-" + w
			code, _, r := in.get("/?to="+to+"&n="+n, tw)
			if code != 200 || r["idName"] != n || r["idEquals"] != true || r["viaGet"] != "get:"+n ||
				r["viaGetByName"] != "getByName:"+n || r["jurisdiction"] != "Jurisdiction restrictions are not implemented in workerd." {
				t.Errorf("%s -> %s: %d %v", w, to, code, r)
			}
		}
	}
}
