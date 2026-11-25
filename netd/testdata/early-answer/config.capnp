# netd's early-answer test (#288): the Worker forwards to a Durable Object
# that answers 410 without reading the request body, like a killed object.
# Addresses are overridden on the command line (see nettest.StartWorkerd).
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "main", worker = .main),
    (name = "netd-egress", external = (
      address = "unix:/nonexistent/egress.sock",
      http = (style = proxy),
    )),
    (name = "do-storage", disk = (path = "/nonexistent/do", writable = true)),
  ],
  sockets = [
    (name = "ingress", address = "unix:/nonexistent/ingress.sock", service = "main"),
  ],
);

const main :Workerd.Worker = (
  compatibilityDate = "2026-04-01",
  modules = [ (name = "main.js", esModule = embed "main.js") ],
  durableObjectNamespaces = [ (className = "Gone", uniqueKey = "tw-early-answer") ],
  durableObjectStorage = (localDisk = "do-storage"),
  bindings = [ (name = "GONE", durableObjectNamespace = "Gone") ],
);
