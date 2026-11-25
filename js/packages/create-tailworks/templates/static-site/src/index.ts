// static-site: a static site (public/) served from a tailworks workload, with
// a Worker next to it. Requests for files in public/ are answered from the
// files; the Worker runs for the paths in wrangler.jsonc
// assets.run_worker_first (/api/* and /team/*) and for paths with no file.
import { getCaller } from "@tailworks/tailnet";

// Env (here: ASSETS, the assets binding) is the global interface `tw types`
// writes to worker-configuration.d.ts. Run `tw types` again after changing
// wrangler.jsonc or tailworks.jsonc.

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const who = getCaller(req);

    // The page's script calls this to greet the visitor.
    if (url.pathname === "/api/whoami") {
      return Response.json({ login: who.user?.login ?? null, node: who.hostname, tags: who.tags });
    }

    // /team/ is for people only: a tagged node (a server, CI) has no user.
    // The identity check runs before any file is served, because /team/*
    // is in run_worker_first.
    if (url.pathname === "/team" || url.pathname.startsWith("/team/")) {
      if (!who.user) {
        return new Response("/team/ is for people on the tailnet, not tagged nodes\n", { status: 403 });
      }
      return env.ASSETS.fetch(req);
    }

    // Anything else: the assets binding applies not_found_handling, so a
    // path with no file gets public/404.html (status 404).
    return env.ASSETS.fetch(req);
  },
} satisfies { fetch(req: Request, env: Env): Promise<Response> };
