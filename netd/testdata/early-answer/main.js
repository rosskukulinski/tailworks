// GET answers 200 (nettest.StartWorkerd's readiness probe). A POST goes to
// a Durable Object that answers 410 without reading the body, the way
// hostd's platform answers for a killed object (#288). POST /read reads
// the body first.
export class Gone {
  async fetch(req) {
    if (new URL(req.url).pathname === "/read") await req.arrayBuffer();
    return new Response("gone", { status: 410 });
  }
}

export default {
  async fetch(req, env) {
    if (req.method === "GET") return new Response("ok");
    return env.GONE.get(env.GONE.idFromName("x")).fetch(req);
  },
};
