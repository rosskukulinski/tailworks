// The e2e/dataplane R2 workload (ledger/r2.md). Every value carries the
// client op ID that wrote it, so a read names the write it saw.
//
//   PUT  /o?k=K            body → B.put(K, body[, {md5}]) → {etag, version, size}
//                          (x-md5: the hex MD5 to send with the put)
//   GET  /o?k=K            → the bytes (x-etag, x-size), or 404 {absent}
//   GET  /h?k=K            → head: {etag, size}, or 404 {absent}
//   DELETE /o?k=K          → B.delete(K)
//   GET  /list?prefix=P    → {keys: [{k, etag, size}]} (every page)
//   POST /mpu/create?k=K   → {uploadId}
//   POST /mpu/part?k=K&u=U&n=N&v=V&size=S → uploadPart(N, gen(V#N|, S)) → {etag}
//   POST /mpu/complete?k=K&u=U  body [{partNumber, etag}] → {etag, size}
//   POST /mpu/abort?k=K&u=U
//
// Errors are 500 {error} with the R2 binding's message (r2_blob_missing
// and the like are in it).
import { DurableObject } from "cloudflare:workers";

// Hold only makes the workload stateful (fenced); see wrangler.jsonc.
export class Hold extends DurableObject {
  fetch() {
    return new Response("ok");
  }
}

// gen is prefix repeated to size bytes (the Go side has the same: r2client_test.go).
function gen(prefix, size) {
  const p = new TextEncoder().encode(prefix);
  const out = new Uint8Array(size);
  for (let i = 0; i < size; i += p.length) out.set(p.subarray(0, Math.min(p.length, size - i)), i);
  return out;
}

async function epochOf(ctx) {
  try {
    return (await ctx.tailnet.self()).epoch ?? 0;
  } catch {
    return 0;
  }
}

export default {
  async fetch(req, env, ctx) {
    const u = new URL(req.url);
    const q = u.searchParams;
    const k = q.get("k");
    const b = env.B;
    const hdr = { "x-epoch": String(await epochOf(ctx)) };
    try {
      if (u.pathname === "/o" && req.method === "PUT") {
        const body = new Uint8Array(await req.arrayBuffer());
        const opts = {};
        if (req.headers.get("x-md5")) opts.md5 = req.headers.get("x-md5");
        const o = await b.put(k, body, opts);
        return Response.json({ etag: o.etag, version: o.version, size: o.size }, { headers: hdr });
      }
      if (u.pathname === "/o" && req.method === "GET") {
        const o = await b.get(k);
        if (!o) return Response.json({ absent: true }, { status: 404, headers: hdr });
        const bytes = await o.arrayBuffer();
        return new Response(bytes, { headers: { ...hdr, "x-etag": o.etag, "x-size": String(o.size) } });
      }
      if (u.pathname === "/o" && req.method === "DELETE") {
        await b.delete(k);
        return Response.json({ ok: true }, { headers: hdr });
      }
      if (u.pathname === "/h") {
        const o = await b.head(k);
        if (!o) return Response.json({ absent: true }, { status: 404, headers: hdr });
        return Response.json({ etag: o.etag, size: o.size }, { headers: hdr });
      }
      if (u.pathname === "/list") {
        const keys = [];
        let cursor;
        for (;;) {
          const r = await b.list({ prefix: q.get("prefix") ?? "", cursor });
          for (const o of r.objects) keys.push({ k: o.key, etag: o.etag, size: o.size });
          if (!r.truncated) break;
          cursor = r.cursor;
        }
        return Response.json({ keys }, { headers: hdr });
      }
      if (u.pathname === "/mpu/create") {
        const m = await b.createMultipartUpload(k);
        return Response.json({ uploadId: m.uploadId }, { headers: hdr });
      }
      if (u.pathname === "/mpu/part") {
        const m = b.resumeMultipartUpload(k, q.get("u"));
        const n = Number(q.get("n"));
        const p = await m.uploadPart(n, gen(q.get("v") + "#" + n + "|", Number(q.get("size"))));
        return Response.json({ etag: p.etag }, { headers: hdr });
      }
      if (u.pathname === "/mpu/complete") {
        const m = b.resumeMultipartUpload(k, q.get("u"));
        const o = await m.complete(await req.json());
        return Response.json({ etag: o.etag, size: o.size }, { headers: hdr });
      }
      if (u.pathname === "/mpu/abort") {
        await b.resumeMultipartUpload(k, q.get("u")).abort();
        return Response.json({ ok: true }, { headers: hdr });
      }
      return new Response("not found", { status: 404 });
    } catch (e) {
      return Response.json({ error: String(e?.message ?? e) }, { status: 500, headers: hdr });
    }
  },
};
