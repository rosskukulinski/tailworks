// A facet's Durable Object namespace bindings (#391).
//
// Inside a facet, env.X is the platform's NsProxy entrypoint stub, which
// has one method, getByName. Any other property of an RPC stub is a remote
// call, so ns.idFromName(n) returns an RpcPromise, and ns.get(id) then
// fails with `Could not serialize object of type "RpcPromise"`. The Agents
// SDK's getAgentByName does exactly that. facetNamespace gives the stub the
// rest of the namespace API, by name, as the Worker's real namespace has.

import { TailnetError } from "./errors.js";
import type { BindingMap } from "./runtime.js";

const BY_NAME = "tw objects must be addressed by name (getByName/idFromName)";

/** FacetId is a facet's DurableObjectId: it carries the object's name, which get() routes by. */
class FacetId {
  readonly name: string;
  constructor(name: string) {
    this.name = name;
  }
  // Not workerd's hex id (only the platform can derive that); unique per
  // class, as the name is.
  toString(): string {
    return this.name;
  }
  equals(other: unknown): boolean {
    return other instanceof FacetId && other.name === this.name;
  }
}

function facetNamespace(ns: any): any {
  return Object.freeze({
    getByName(name: unknown) {
      return ns.getByName(String(name));
    },
    idFromName(name: unknown) {
      return new FacetId(String(name));
    },
    idFromString(_s: unknown): never {
      throw new TailnetError("bad_request", `idFromString: ${BY_NAME}`);
    },
    newUniqueId(_opts?: unknown): never {
      throw new TailnetError("bad_request", `newUniqueId: ${BY_NAME}; newUniqueId() is not supported yet`);
    },
    get(id: unknown, _opts?: unknown) {
      const n = (id as any)?.name;
      if (!(id instanceof FacetId) || typeof n !== "string" || !n) throw new TailnetError("bad_request", `get: ${BY_NAME}`);
      return ns.getByName(n);
    },
    jurisdiction(_j: unknown): never {
      // As the Worker's own namespace does on workerd.
      throw new Error("Jurisdiction restrictions are not implemented in workerd.");
    },
  });
}

/** facetNamespaces returns a facet's env with each NsProxy binding given the namespace API. */
export function facetNamespaces(env: any, bindings: BindingMap): any {
  if (!env || typeof env !== "object") return env;
  const out: Record<string, unknown> = { ...env };
  for (const k of Object.keys(bindings)) if (env[k]) out[k] = facetNamespace(env[k]);
  return out;
}
