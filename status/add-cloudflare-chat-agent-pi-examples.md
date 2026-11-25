# add-cloudflare-chat-agent-pi-examples

Two create-tailworks templates from Cloudflare's Agents docs:

- `chat-agent`: https://developers.cloudflare.com/agents/examples/chat-agent/
  (`AIChatAgent`, `getWeather` / `getUserTimezone` / `calculate` with
  `needsApproval`). Adds `POST /` / `GET /?message=` (one turn via
  `saveMessages`) so it runs from curl and tests without a browser.
- `pi-agent`: https://developers.cloudflare.com/agents/harnesses/pi/
  (`PiHarness` + `Lifecycle` on a plain `DurableObject`, `word_count` tool).

Both use a scripted model (AI SDK `MockLanguageModelV4`; pi-ai's faux
provider) because tailworks has no Workers AI binding. One agent per
caller (`principalKey`), each its own node (`"agent": true`).

`agents` 0.33.0 → 0.34.0 everywhere (Pi needs ≥ 0.34). New root devDeps:
`@cloudflare/ai-chat` 0.12.3, `@earendil-works/{pi-durable,pi-ai,pi-schema,chord}` 1.6.0.
Vendored release tree: 264 → 265 packages, 160.3 → 161 MB; `check-pure-js` ok.

Follow-ups (same day):

- Chat page at `/` (`src/page.ts`, dependency-free, speaks useAgentChat's
  WebSocket protocol; tailworks ignores wrangler `assets`). Driven in a
  browser under `tw dev`: streaming, getWeather, getUserTimezone answered
  by the page, Approve on a large calculate. The template test covers the
  same over a WebSocket.
- `routeAgentRequest` doesn't decode the instance name, so `/agents/chat-agent/user%40…`
  and `getAgentByName("user@…")` were different objects; chats are named by
  the URL-encoded `principalKey`.
- Aperture: `vars.LLM_URL` / `LLM_MODEL` select an OpenAI-compatible gateway
  (`@ai-sdk/openai-compatible` 3.0.94 with `tailnet(ctx).fetch`; for Pi a
  pi-ai provider on `openAICompletionsApi`, placeholder key). Empty = mock.
  Checked against a fake OpenAI-compatible server (both send
  `POST /v1/chat/completions` with the model); not yet against a real Aperture.

## Gaps

- Not run against a real Aperture on a real tailnet.
- Pi: Lifecycle alarm resuming a run after eviction not tested.

## Tests

2026-11-23, on this branch (master 8fed1e2a):

- `make lint`: ok.
- `make test`: test-go ok after wrapping the `tw help` templates line
  (TestHelpGroups' 130-character limit; `go test -race ./cmd/...` rerun ok);
  `make test-js` ok.
- `go test -run 'TestTemplateTests/(chat-agent|pi-agent)' ./cmd/tw`: ok.
- `make templates-typecheck`: all 9 templates ok.
- `make test-sdk`: ok (agents 0.34.0).
- Vendored release tree + `scripts/check-pure-js.mjs`: ok.
- Not run: `make e2e-*` real-tailnet suites (no change to deploy paths).

After the chat page and Aperture changes:

- `scripts/templates-typecheck.sh chat-agent pi-agent`: ok.
- `TestTemplateTests/(chat-agent|pi-agent)` (chat-agent now 4 tests incl. the
  WebSocket approval/client-tool flow): ok.
- The page in Chromium (Playwright) under `tw dev`: ok.
- Both model paths against a fake OpenAI-compatible server: ok.
- `make lint`, `make changes-check`, `make test-js`: ok.
- Vendored tree: 267 packages, 163.1 MB; `check-pure-js` ok.

Final, after merging origin/master (4ffbb469; #277, merged since, only changes ci.yml):

- `make lint`, `make test`, `make templates-typecheck` (9 templates), `make test-sdk`: all ok.
- Static assets support split out to issue #278.

## 2026-11-25: merged origin/master, fixed CI

CI (2026-11-24) failed in every Node job at `npm ci`: the lockfile was
written by npm 11, which leaves out the optional `@emnapi/core` /
`@emnapi/runtime` peers that CI's npm 10 requires. `package-lock.json` is
now regenerated with npm 10.9.9 (CI's); `npm ci` passes under npm 10 and 11.

Merge resolution: master moved the template list into `cmd/tw/templates.go`
(four starting points in help, the rest under `tw init --list`), so
chat-agent and pi-agent are two rows in that table (demos) instead of
edits to the usage and help lines. Both templates now pin
`@tailworks/tailnet ^0.5.0` and workers-types `5.20261121.1`, like the
others. COMPAT.md keeps master's upstream-examples rows; the
`AIChatAgent` "not tested" row is replaced by the template-tested rows.
The `tw-scaffold` skill lists both templates.

Tests (merged tree):

- `make lint`: ok.
- `make test` (offloaded to mbp): ok.
- `make templates-typecheck`: all 11 templates ok (`tw types --check` ok; the
  committed worker-configuration.d.ts files were already current).
- `make test-sdk`: ok.
- `make npm-cli-test`: ok.
- `make demo-test`: PASS.
- `go test ./cmd/tw -run 'TestTemplateTests|TestCreateTailworksRenamesLikeInit|TestInitTemplatesTable|TestInitList|TestHelp'`: ok, chat-agent and pi-agent included.
- Not run: `make e2e-compat` (real tailnet, writes policy; the suite's
  projects pin their own deps and its upstream examples already use
  agents 0.34.0).
