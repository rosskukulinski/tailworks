
// ---- Cloudflare Pages Functions runtime (internal/pages/runtime.js) ----
// Follows wrangler's pages-template-worker: middlewares of every route
// that matches as a prefix run first (least specific first), then the
// handlers of the first route that matches exactly; next() with nothing
// left falls through to the assets binding.

const __twPagesMethods = {
  onRequest: "",
  onRequestGet: "GET",
  onRequestPost: "POST",
  onRequestPut: "PUT",
  onRequestPatch: "PATCH",
  onRequestDelete: "DELETE",
  onRequestHead: "HEAD",
  onRequestOptions: "OPTIONS",
};

// __twPagesMatcher compiles a wrangler route path ("/api/:id",
// "/files/:path*") as path-to-regexp does for it: case-insensitive, an
// optional trailing slash, :x* for zero or more segments (an array).
function __twPagesMatcher(routePath, end) {
  const keys = [];
  let src = "";
  for (const seg of routePath.split("/").filter(Boolean)) {
    let m;
    if ((m = /^:(\w+)\*$/.exec(seg))) {
      keys.push({ name: m[1], repeat: true });
      src += "(?:/([^/]+?(?:/[^/]+?)*))?";
    } else if ((m = /^:(\w+)$/.exec(seg))) {
      keys.push({ name: m[1], repeat: false });
      src += "/([^/]+?)";
    } else {
      src += "/" + seg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    }
  }
  const re = new RegExp("^" + src + (end ? "/?$" : "/?(?=/|$)"), "i");
  return (path) => {
    const r = re.exec(path);
    if (!r) return null;
    const params = {};
    keys.forEach((k, i) => {
      const v = r[i + 1];
      if (v !== undefined) params[k.name] = k.repeat ? v.split("/") : v;
    });
    return { path: r[0] || "/", params };
  };
}

// __twPagesCompare is wrangler's route order: more segments first, then
// literal before parameter before catch-all, then a route with a method
// before one without, then by path.
function __twPagesCompare(a, b) {
  const sa = a.routePath.slice(1).split("/").filter(Boolean);
  const sb = b.routePath.slice(1).split("/").filter(Boolean);
  if (sa.length !== sb.length) return sb.length - sa.length;
  for (let i = 0; i < sa.length; i++) {
    const wa = sa[i].includes("*"), wb = sb[i].includes("*");
    if (wa !== wb) return wa ? 1 : -1;
    const pa = sa[i].includes(":"), pb = sb[i].includes(":");
    if (pa !== pb) return pa ? 1 : -1;
  }
  if (a.method && !b.method) return -1;
  if (!a.method && b.method) return 1;
  return a.routePath < b.routePath ? -1 : a.routePath > b.routePath ? 1 : 0;
}

function __twPagesCompile(files) {
  const routes = [];
  for (const f of files) {
    for (const [name, method] of Object.entries(__twPagesMethods)) {
      const h = f.module[name];
      if (h === undefined) continue;
      const handlers = [h].flat();
      if (handlers.some((x) => typeof x !== "function")) {
        throw new Error(`functions/${f.file}: ${name} must be a function or an array of functions`);
      }
      routes.push({
        file: f.file,
        routePath: f.routePath,
        mountPath: f.mountPath,
        method,
        middleware: f.middleware,
        handlers,
        prefix: __twPagesMatcher(f.routePath, false),
        exact: __twPagesMatcher(f.routePath, true),
        mount: __twPagesMatcher(f.mountPath, false),
      });
    }
  }
  return routes.sort(__twPagesCompare);
}

function* __twPagesExecute(routes, request) {
  const path = new URL(request.url).pathname;
  for (const r of [...routes].reverse()) {
    if (!r.middleware || (r.method && r.method !== request.method)) continue;
    const m = r.prefix(path), mm = r.mount(path);
    if (m && mm) for (const handler of r.handlers) yield { handler, params: m.params, path: mm.path };
  }
  for (const r of routes) {
    if (r.middleware || (r.method && r.method !== request.method)) continue;
    const m = r.exact(path), mm = r.mount(path);
    if (m && mm) {
      for (const handler of r.handlers) yield { handler, params: m.params, path: m.path };
      break;
    }
  }
}

// __twPagesRouted: does _routes.json send this path to the Functions?
function __twPagesRouted(filter, path) {
  if (!filter) return true;
  const hit = (ps) =>
    ps.some((p) => new RegExp("^" + p.split("*").map((s) => s.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$").test(path));
  return !hit(filter.exclude) && hit(filter.include);
}

// A Response a handler returned may be immutable (fetch's): copy it.
const __twPagesClone = (r) => new Response([101, 204, 205, 304].includes(r.status) ? null : r.body, r);

function __twPagesFunctions(files, binding, filter) {
  const routes = __twPagesCompile(files);
  return {
    async fetch(originalRequest, env, ctx) {
      let request = originalRequest;
      const assets = (req) => {
        const a = env[binding];
        if (!a) throw new Error(`env.${binding} (the Pages assets binding) is missing`);
        return a.fetch(req);
      };
      if (!__twPagesRouted(filter, new URL(request.url).pathname)) return assets(request);
      const it = __twPagesExecute(routes, request);
      let data = {};
      let failOpen = false;
      const next = async (input, init) => {
        if (input !== undefined) {
          let url = input;
          if (typeof input === "string") url = new URL(input, request.url).toString();
          request = new Request(url, init);
        }
        const step = it.next();
        if (step.done) return __twPagesClone(await assets(request));
        const { handler, params, path } = step.value;
        const context = {
          request: new Request(request.clone()),
          functionPath: path,
          next,
          params,
          get data() {
            return data;
          },
          set data(v) {
            if (typeof v !== "object" || v === null) throw new Error("context.data must be an object");
            data = v;
          },
          env,
          waitUntil: ctx.waitUntil.bind(ctx),
          passThroughOnException: () => {
            failOpen = true;
          },
        };
        const response = await handler(context);
        if (!(response instanceof Response)) throw new Error("Your Pages function should return a Response");
        return __twPagesClone(response);
      };
      try {
        return await next();
      } catch (e) {
        if (failOpen) return __twPagesClone(await assets(request));
        throw e;
      }
    },
  };
}

function __twPagesAdvanced(worker, binding, filter) {
  if (!filter || !worker || typeof worker !== "object" || typeof worker.fetch !== "function") return worker;
  return {
    ...worker,
    fetch(req, env, ctx) {
      if (!__twPagesRouted(filter, new URL(req.url).pathname)) return env[binding].fetch(req);
      return worker.fetch.call(worker, req, env, ctx);
    },
  };
}
// ---- end of the Pages runtime ----
