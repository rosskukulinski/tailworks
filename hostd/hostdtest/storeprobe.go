package hostdtest

import (
	"encoding/json"
	"sync"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// ProbeStoreKey is the wrangler.jsonc key of the test-only "probe" store
// kind: [{"binding": "P", "name": "main"}].
const ProbeStoreKey = "tw_test_probes"

var probeOnce sync.Once

// RegisterProbeStore registers the "probe" store kind (tests of the
// platform-store substrate, ADR-044): a last-writer-wins map
// behind a service binding with put/get/del/delIf/all, built on the same
// Store object, journal and catch-up as the real kinds.
func RegisterProbeStore() {
	probeOnce.Do(func() {
		bundle.RegisterStoreKind(bundle.StoreKind{Kind: "probe", WranglerKey: ProbeStoreKey,
			Parse: func(raw json.RawMessage) ([]bundle.StoreBinding, error) {
				var in []struct{ Binding, Name string }
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				out := make([]bundle.StoreBinding, len(in))
				for i, b := range in {
					out[i] = bundle.StoreBinding{Binding: b.Binding, Name: b.Name}
				}
				return out, nil
			}})
		gen.RegisterStoreKind(gen.StoreKindGen{Kind: "probe", Binding: "service", Backend: "ProbeBackend", JS: probeStoreJS})
	})
}

const probeStoreJS = `import { WorkerEntrypoint } from "cloudflare:workers";
import { registerStoreKind, storeStub, utf8 } from "tw-store.js";

registerStoreKind("probe", {
  init(s) {
    s.sql.exec("CREATE TABLE IF NOT EXISTS probe_items (k TEXT PRIMARY KEY, v TEXT NOT NULL, seq INTEGER NOT NULL)");
  },
  apply(s, data, seq) {
    const e = JSON.parse(utf8(data));
    if (e.del) s.sql.exec("DELETE FROM probe_items WHERE k = ?", e.k);
    else s.sql.exec("INSERT INTO probe_items (k, v, seq) VALUES (?, ?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v, seq = excluded.seq", e.k, e.v, seq);
  },
  reset(s) {
    s.sql.exec("DELETE FROM probe_items");
  },
  ops: {
    put(s, k, v) {
      return s.commit([{ ckey: k, data: JSON.stringify({ k, v }) }]);
    },
    del(s, k) {
      return s.commit([{ ckey: k, data: JSON.stringify({ k, del: true }) }]);
    },
    // delIf writes only if the key is there, as R2 and KV deletes do.
    delIf(s, k) {
      return s.commit(() => (s.sql.exec("SELECT 1 FROM probe_items WHERE k = ?", k).toArray().length ? [{ ckey: k, data: JSON.stringify({ k, del: true }) }] : []));
    },
    async get(s, k) {
      await s.fresh();
      const r = s.sql.exec("SELECT v FROM probe_items WHERE k = ?", k).toArray();
      return r.length ? r[0].v : null;
    },
    async all(s) {
      await s.fresh(0);
      return Object.fromEntries(s.sql.exec("SELECT k, v FROM probe_items ORDER BY k").toArray().map((r) => [r.k, r.v]));
    },
  },
});

export class ProbeBackend extends WorkerEntrypoint {
  #s() {
    return storeStub(this.ctx.exports, this.ctx.props.kind, this.ctx.props.name);
  }
  put(k, v) { return this.#s().op("put", [k, v]); }
  del(k) { return this.#s().op("del", [k]); }
  delIf(k) { return this.#s().op("delIf", [k]); }
  get(k) { return this.#s().op("get", [k]); }
  all() { return this.#s().op("all", []); }
}
`
