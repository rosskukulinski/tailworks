// Static assets in front of the app's Worker (ADR-050): the
// tenant service's default export is withAssets(<wrapped app handler>).
// A GET or HEAD to the workload asks the assets router (js/assets.js) first;
// a file or redirect is the answer, a miss runs the app as before. Cron
// runs, queue deliveries and object requests never reach the router.
// run_worker_first: true skips it (the app calls env.ASSETS itself).
const MISS = "x-tw-asset-miss";

function routed(req) {
  return req?.cf?.tw?.kind === "workload" && (req.method === "GET" || req.method === "HEAD");
}

async function route(router, req) {
  if (!router || !routed(req)) return null;
  const r = await router.fetch(req);
  return r.headers.get(MISS) ? null : r;
}

const noWorker = () => new Response("Not Found", { status: 404 });

export function withAssets(H, workerFirstAll) {
  if (workerFirstAll) return H;
  if (typeof H === "function") {
    const f = H.prototype?.fetch;
    const C = class extends H {
      constructor(ctx, env) {
        super(ctx, env);
        this.__twAssets = env?.__TW_ASSETS;
      }
    };
    C.prototype.fetch = async function (req) {
      const r = await route(this.__twAssets, req);
      if (r) return r;
      return f ? f.call(this, req) : noWorker();
    };
    return C;
  }
  return {
    ...H,
    async fetch(req, env, ctx) {
      const r = await route(env?.__TW_ASSETS, req);
      if (r) return r;
      return typeof H?.fetch === "function" ? H.fetch.call(H, req, env, ctx) : noWorker();
    },
  };
}
