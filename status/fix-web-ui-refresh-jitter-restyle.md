Last update: 2026-11-15 09:10 PT. Admin UI uses only the public /v1 API.

## Admin UI: public API only (`mgmt/ui`, `mgmt/uiclient.go`)

Goal: the UI should depend on nothing a separate process couldn't use, so
it can be extracted later. It already called only documented endpoints and
query parameters. The private couplings were: an in-memory call into mgmt's
API handler that relied on mgmt's context-held principal (and copied
`RemoteAddr`/`Host`), an undocumented `Twd-Trace-ID` header, admission left
entirely to mgmt's `mountUI`, and browser JavaScript calling `/v1` directly
(which only works same-origin with mgmt).

Now:

- `ui.Handler(*http.Client, baseURL)`: the UI is a plain HTTP client and
  sends only `Accept` and `Twd-Actor` (both documented).
- mgmt hands it `uiTransport` (new `mgmt/uiclient.go`), which serves each
  request with the API handler in-process, using the browser request's
  context. The identity hand-off now lives in mgmt, its owner.
- The UI checks admission itself via `GET /v1/whoami` (admin, not a host).
  mgmt's gate stays as a second layer.
- The browser never calls `/v1`: `app.js` only confirms, then the form
  POSTs to `/ui/...` and the UI calls the API server-side. The toast code
  is gone; outcomes show as the existing flash banner.
- `TestUsesOnlyPublicAPI` (`mgmt/ui/public_api_test.go`) records every API
  request while rendering every page and submitting every form, and checks
  each operation, query parameter and header against `api/openapi.yaml`.
  It also fails if a page contains a `/v1` URL. I checked that it catches
  an undocumented query parameter and header. `TestUISpecConformance` now
  also runs the four form mutations through the kin-openapi validator.
- `browserGuard` behaviour is unchanged. No first-party code sends
  `X-Twd-UI` any more, so browser mutations of `/v1` are effectively
  same-origin opt-in only; tightening it to "refuse all" is a possible
  follow-up.

Open question for real extraction: an out-of-process UI would call the API
as its own node, not as the browser's user. It needs a delegation mechanism
in the API, which isn't decided (noted in `docs/ADMIN-UI.md`).

Tests: `make lint` ok; `go test -race -count=1 ./mgmt/...` ok.

---

2026-11-15 08:20 PT. Admin UI: stop the refresh jitter, readability redesign.

## Admin web UI (`mgmt/ui`)

**Jitter fix.** The live refresh re-fetched the page every 3 s and swapped any
`[data-live]` region whose HTML differed. Relative times ("5s", "8s") are part
of that HTML, so every table changed on every fetch and was rebuilt, losing
hover and selection and shifting layout. Now:

- Relative times render as `<time datetime=… data-ago>` (new `since` template
  func in `views.go`). `app.js` recomputes their text in the browser for both
  the fresh and the current DOM before it compares, so a region is swapped only
  when its data changed.
- Refresh every 5 s (was 3 s). A region with selected text is left alone, the
  page refreshes immediately when the tab becomes visible again, and the
  redundant `/v1/status` fetch is gone.
- The footer shows a steady "Live" dot instead of a timestamp that changed
  every tick. It turns red with the error when a refresh fails.

Checked in a browser against the populated fixture: over 12 s (two refreshes),
0 region swaps, and only the `<time>` texts changed.

**Readability.** 14 px base font, roomier tables, sentence-case headings, each
with a one-line explanation. Column labels are plain words ("Last heartbeat",
"Nodes (live / capacity)", "What happened"), and status badges show a coloured
dot. The overview opens with summary tiles (hosts online, deployments,
tenants, objects, audit integrity), then deployments, hosts, tenants, objects
and recent activity. Mgmt self-info moved into a collapsed "Control plane
details" panel. Header stats were dropped in favour of the tiles. Dangerous
actions (kill, undeploy, remove host, delete tenant) sit in red-bordered cards
at the bottom of their pages.

Tests: `go test ./mgmt/...` passes. One test string changed to the new heading
case ("Node certificate"), and a `since` case was added to `TestHelpers`.
