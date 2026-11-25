Last update: 2026-11-25 12:10 PT. One typed renderer for policy snippets; merge comment before every pasted block (#535).

## What changed

- `internal/policy/render.go`: `EntryHuJSON` (a grant or nodeAttrs entry,
  keys in src, dst, target, ip, attr, app order, with spaces),
  `Snippet.Inline` (comment-free, one line per section: the API string
  fields), `Snippet.HuJSON` (one entry per line, now with ordered grants),
  `Snippet.Paste(notes)` (HuJSON led by `policy.MergeComment`, a comment
  after each grant). `ListHuJSON` for one-line tagOwners entries.
- mgmt (`mgmt/preflightsnippet.go`): the preflight builds its parts as typed
  snippets (tagOwners via `tagOwnersFor`, grants, the public part derived
  from `policy.PublicSnippet`, the Service approver) and renders the string
  fields from them. `PreflightResult.Snippet` (new `snippet` object in
  `api/openapi.yaml`, schema `PolicySnippet`) holds all of them;
  `PreflightMissing.IP` carries the port. `preflightSnippet` and
  `serviceApprover` read the typed snippet; `parseHuJSONObject` is gone.
  `policy.Check` still runs per entry; the caller grant is still built by
  mgmt after Check and never in the typed object. `PublicFunnelSnippet` and
  `publicFunnelSnippet` are gone. `HostTagOwnersSnippet(t)` is
  Headscale-aware. `callerGrant` / the `callers:` manual line use
  `EntryHuJSON`.
- CLI (`cmd/tw/policysnippet.go`): `preflightPolicy(pf)` is the typed
  snippet, or for a mgmt older than this, a snippet read from the string
  fields. `tw policy`'s block, the deploy preflight (connects, public,
  service parts), the `acl_denied`/`funnel_required` fix (now one block),
  `tw up`'s `snippetLines` and `tagOwnersFix` all render through
  `policy`.

## Not done / notes

- The verifier note "list only tag:tw-host in CreateHost's snippet (E4)" was
  already true on master (#443); only the Headscale owners were missing.
- `missingIP` (re-reading a missing grant's port from its snippet) stays
  as a fallback for results from an older mgmt (CLI newer than mgmt).

## Tests

See the PR's Tests section.
