import { DurableObject } from "cloudflare:workers";

const J = (v, status = 200) => Response.json(v, { status });

function objJSON(o) {
  if (o === null || o === undefined) return null;
  const h = new Headers();
  o.writeHttpMetadata(h);
  const hm = { ...o.httpMetadata };
  if (hm.cacheExpiry) hm.cacheExpiry = new Date(hm.cacheExpiry).getTime();
  return {
    key: o.key,
    size: o.size,
    etag: o.etag,
    httpEtag: o.httpEtag,
    version: o.version,
    uploaded: o.uploaded.getTime(),
    httpMetadata: hm,
    customMetadata: o.customMetadata,
    range: o.range,
    checksums: o.checksums.toJSON(),
    headers: Object.fromEntries(h),
    storageClass: o.storageClass,
    hasBody: "body" in o,
  };
}

const unb64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const hex = (b) => [...new Uint8Array(b)].map((x) => x.toString(16).padStart(2, "0")).join("");

// opts turns JSON options into R2 options (dates, Headers forms).
function opts(o) {
  if (!o) return o;
  const r = { ...o };
  if (r.onlyIf) {
    r.onlyIf = { ...r.onlyIf };
    for (const k of ["uploadedBefore", "uploadedAfter"]) if (r.onlyIf[k] !== undefined) r.onlyIf[k] = new Date(r.onlyIf[k]);
  }
  if (r.onlyIfHeaders) {
    r.onlyIf = new Headers(r.onlyIfHeaders);
    delete r.onlyIfHeaders;
  }
  if (r.rangeHeader) {
    r.range = new Headers({ range: r.rangeHeader });
    delete r.rangeHeader;
  }
  if (r.httpMetadata?.cacheExpiry) r.httpMetadata = { ...r.httpMetadata, cacheExpiry: new Date(r.httpMetadata.cacheExpiry) };
  if (r.httpHeaders) {
    r.httpMetadata = new Headers(r.httpHeaders);
    delete r.httpHeaders;
  }
  for (const k of ["sha1", "sha256"]) if (r[k + "Hex"]) (r[k] = r[k + "Hex"]), delete r[k + "Hex"];
  return r;
}

// pattern returns a stream of n bytes (byte i = i * 7 mod 251), made as it
// is read, of known length.
function pattern(n) {
  const { readable, writable } = new FixedLengthStream(n);
  (async () => {
    const w = writable.getWriter();
    const chunk = 64 * 1024;
    for (let off = 0; off < n; off += chunk) {
      const len = Math.min(chunk, n - off);
      const b = new Uint8Array(len);
      for (let i = 0; i < len; i++) b[i] = ((off + i) * 7) % 251;
      await w.write(b);
    }
    await w.close();
  })();
  return readable;
}

async function serve(env, req, u) {
  const B = env.B;
  const p = u.pathname.replace(/^\/do/, "") || "/";
  if (p === "/op") {
    const a = await req.json();
    const B = env[a.bucket ?? "B"];
    switch (a.op) {
      case "put": {
        const v = a.b64 !== undefined ? unb64(a.b64) : a.value;
        return J(objJSON(await B.put(a.key, v, opts(a.opts))));
      }
      case "get": {
        const o = await B.get(a.key, opts(a.opts));
        const j = objJSON(o);
        if (j && j.hasBody) j.text = await o.text();
        return J(j);
      }
      case "head":
        return J(objJSON(await B.head(a.key)));
      case "delete":
        await B.delete(a.keys ?? a.key);
        return J({ ok: true });
      case "list": {
        const r = await B.list(opts(a.opts));
        return J({ ...r, objects: r.objects.map(objJSON) });
      }
      case "mpu": {
        // parts: [size…]; part i is filled with byte (65 + i).
        const up = await B.createMultipartUpload(a.key, opts(a.opts));
        const resumed = B.resumeMultipartUpload(a.key, up.uploadId);
        const parts = [];
        for (let i = 0; i < a.parts.length; i++) parts.push(await resumed.uploadPart(i + 1, new Uint8Array(a.parts[i]).fill(65 + i)));
        if (a.abort) {
          await up.abort();
          return J({ aborted: true, uploadId: up.uploadId });
        }
        const sel = a.badEtag ? [{ partNumber: 1, etag: "nope" }] : parts;
        return J({ uploadId: up.uploadId, parts, object: objJSON(await up.complete(sel)) });
      }
    }
    return J({ error: "unknown op" }, 400);
  }
  // Streaming: a generated body of ?size bytes, put as a stream of known
  // length; read back and hashed as a stream.
  if (p === "/gen") {
    const n = Number(u.searchParams.get("size"));
    return J(objJSON(await B.put(u.searchParams.get("key"), pattern(n))));
  }
  if (p === "/copy") {
    // object → object, body stream to body stream (no JS buffers)
    const o = await B.get(u.searchParams.get("from"));
    return J(objJSON(await B.put(u.searchParams.get("to"), o.body)));
  }
  if (p === "/sum") {
    const o = await B.get(u.searchParams.get("key"));
    if (!o) return J(null, 404);
    const d = new crypto.DigestStream("SHA-256");
    await o.body.pipeTo(d);
    return J({ size: o.size, sha256: hex(await d.digest) });
  }
  if (p === "/stream") {
    const key = u.searchParams.get("key");
    if (req.method === "PUT") return J(objJSON(await B.put(key, req.body)));
    const o = await B.get(key);
    if (!o) return J(null, 404);
    const h = new Headers();
    o.writeHttpMetadata(h);
    h.set("etag", o.httpEtag);
    return new Response(o.body, { headers: h });
  }
  return new Response("not found", { status: 404 });
}

async function run(env, req) {
  try {
    return await serve(env, req, new URL(req.url));
  } catch (e) {
    return J({ error: String(e?.message ?? e) }, 500);
  }
}

export class Box extends DurableObject {
  fetch(req) {
    return run(this.env, req);
  }
}

export default {
  fetch(req, env) {
    if (new URL(req.url).pathname.startsWith("/do/")) return env.BOX.getByName("b1").fetch(req);
    return run(env, req);
  },
};
