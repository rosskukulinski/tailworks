---
kind: feature
upgrade: [feature-string, config-default]
---
A workload can now be served over HTTPS on the tailnet: set
`"tailnet": {"https": true}` in tailworks.jsonc and the workload node also
listens on `https://<name>.<tailnet>.ts.net` (port 443) with its own ts.net
certificate. A request that arrives that way reaches the Worker with an
`https://` `request.url` and `X-Forwarded-Proto: https` (a Node-tier app
gets the header), so code that insists on an HTTPS origin runs unmodified:
`@cloudflare/workers-oauth-provider`'s resource URL, `Secure` cookies,
OAuth redirect URIs (#360). `http://` on port 80 keeps working, unchanged
and not redirected; `tw deploy` prints the `https://` URL. For such an app
the caller grant that `tw deploy` and `tw policy` print, and that
`--apply-policy` / `tw policy --apply` write for a new app, covers
`tcp:443` as well as `tcp:80`. Apps without the setting keep `tcp:80` only.

The tailnet needs HTTPS certificates (and MagicDNS). Without them the
deploy fails with `tailnet_https_unavailable` and the active deployment
keeps serving. Headscale issues no ts.net certificates. Only the workload
node gets the listener: objects' own nodes and Tailscale Service names stay
`http://`. `tw dev` serves its local `http://` URL only and says so.

**Behaviour change for public routes:** a request on a public (Funnel)
route now has an `https://` `request.url`, the URL the client called, as
on Cloudflare. It used to be `http://<name>.<tailnet>.ts.net/…` with
`X-Forwarded-Proto: https`. Code that compared `request.url` with an
`https://` origin now matches without a workaround; code that rebuilt the
URL from `X-Forwarded-Proto` keeps working. Code that expected `http://`
in a public request's URL (string-building redirects or callback URLs
from it, say) now sees `https://`.

Operator action: a bundle with `tailnet.https` is placed only on hosts that
offer the `tailnet-https` feature; upgrade the hosts first. If an app's
caller grant was written before you turned `tailnet.https` on, or you wrote
it yourself, add `"tcp:443"` to its `ip`.
