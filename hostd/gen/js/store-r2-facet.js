// tailworks R2 binding inside a facet-mode object (a Dynamic Worker; runs with
// the tenant's code). ADR-036.
//
// A facet can't be handed workerd's native r2Bucket binding, so its
// env.<Binding> is an RPC stub to the platform's R2FacetProxy, which calls
// the platform's native binding of the same bucket and returns plain
// objects. wrap() turns that stub back into an R2Bucket-like object whose
// results are R2Object / R2ObjectBody with their methods (text(), json(),
// writeHttpMetadata(), …). It holds no credential and can only reach the
// bucket the stub is bound to.

const HTTP_HEADERS = [
  ["contentType", "content-type"],
  ["contentLanguage", "content-language"],
  ["contentDisposition", "content-disposition"],
  ["contentEncoding", "content-encoding"],
  ["cacheControl", "cache-control"],
];

class R2Checksums {
  constructor(c) {
    for (const [k, v] of Object.entries(c ?? {})) this[k] = v;
  }
  toJSON() {
    const o = {};
    for (const [k, v] of Object.entries(this)) o[k] = [...new Uint8Array(v)].map((x) => x.toString(16).padStart(2, "0")).join("");
    return o;
  }
}

export class R2Object {
  constructor(p) {
    this.key = p.key;
    this.version = p.version;
    this.size = p.size;
    this.etag = p.etag;
    this.httpEtag = p.httpEtag;
    this.uploaded = p.uploaded;
    this.httpMetadata = p.httpMetadata ?? {};
    this.customMetadata = p.customMetadata ?? {};
    this.range = p.range;
    this.checksums = new R2Checksums(p.checksums);
    this.storageClass = p.storageClass;
  }
  writeHttpMetadata(headers) {
    for (const [f, h] of HTTP_HEADERS) if (this.httpMetadata[f] !== undefined) headers.set(h, this.httpMetadata[f]);
    if (this.httpMetadata.cacheExpiry !== undefined) headers.set("expires", new Date(this.httpMetadata.cacheExpiry).toUTCString());
  }
}

export class R2ObjectBody extends R2Object {
  #body;
  #used = false;
  constructor(p) {
    super(p);
    this.#body = p.body;
  }
  get body() {
    return this.#body;
  }
  get bodyUsed() {
    return this.#used;
  }
  #take() {
    if (this.#used) throw new TypeError("This object's body has already been read.");
    this.#used = true;
    return new Response(this.#body);
  }
  arrayBuffer() {
    return this.#take().arrayBuffer();
  }
  async bytes() {
    return new Uint8Array(await this.#take().arrayBuffer());
  }
  text() {
    return this.#take().text();
  }
  json() {
    return this.#take().json();
  }
  blob() {
    return this.#take().blob();
  }
}

// A Blob can't cross RPC; its bytes can (a Blob is in memory anyway).
async function value(v) {
  if (typeof Blob !== "undefined" && v instanceof Blob) return v.arrayBuffer();
  return v;
}

class R2MultipartUpload {
  #stub;
  constructor(stub, key, uploadId) {
    this.#stub = stub;
    this.key = key;
    this.uploadId = uploadId;
  }
  async uploadPart(partNumber, v, options) {
    return this.#stub.uploadPart(this.key, this.uploadId, partNumber, await value(v), options);
  }
  async abort() {
    await this.#stub.abortMultipartUpload(this.key, this.uploadId);
  }
  async complete(parts) {
    return new R2Object(await this.#stub.completeMultipartUpload(this.key, this.uploadId, parts));
  }
}

class R2Bucket {
  #stub;
  constructor(stub) {
    this.#stub = stub;
  }
  async head(key) {
    const p = await this.#stub.head(key);
    return p ? new R2Object(p) : null;
  }
  async get(key, options) {
    const p = await this.#stub.get(key, options);
    if (!p) return null;
    return p.body !== undefined ? new R2ObjectBody(p) : new R2Object(p);
  }
  async put(key, v, options) {
    const p = await this.#stub.put(key, await value(v), options);
    return p ? new R2Object(p) : null;
  }
  async delete(keys) {
    await this.#stub.delete(keys);
  }
  async list(options) {
    const r = await this.#stub.list(options);
    return { ...r, objects: r.objects.map((p) => new R2Object(p)) };
  }
  async createMultipartUpload(key, options) {
    const u = await this.#stub.createMultipartUpload(key, options);
    return new R2MultipartUpload(this.#stub, u.key, u.uploadId);
  }
  resumeMultipartUpload(key, uploadId) {
    return new R2MultipartUpload(this.#stub, key, uploadId);
  }
}

export function wrap(stub) {
  return stub ? new R2Bucket(stub) : stub;
}
