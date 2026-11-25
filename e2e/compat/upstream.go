package compat

// The upstream examples of issue #325: cloudflare/agents `examples/` (the
// playground is one of them), vercel/mcp-handler `examples/` and the Vercel
// AI SDK's server examples. Each one that runs is a Project with a Section
// (the COMPAT.md table it is published in); each one that doesn't is in
// UpstreamNotRun with the reason. report.go renders both, with the results
// of the last committed run, into docs/COMPAT.md.

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The pinned upstream commits and the published versions the examples'
// workspace dependencies ("*", "workspace:*") are pinned to: the versions
// those repos carry at these commits.
const (
	mcpHandlerCommit = "5518e8af4386a92756d189169a97601a4a7162ee" // vercel/mcp-handler main, 2026-11-18
	vercelAICommit   = "8956ca55ed9c31b4b7680621a10ad0269f9629b1" // vercel/ai main, 2026-11-23

	AgentsVersion     = "0.34.0"  // cloudflare/agents packages/agents at agentsCommit
	MCPServerVersion  = "2.4.0"   // @modelcontextprotocol/server (MCP SDK v2)
	MCPSDKVersion     = "1.32.2"  // @modelcontextprotocol/sdk (MCP SDK v1)
	McpHandlerVersion = "2.4.0"   // vercel/mcp-handler at mcpHandlerCommit
	AIVersion         = "7.0.181" // vercel/ai packages/ai at vercelAICommit
	AIOpenAIVersion   = "4.0.122" // vercel/ai packages/openai at vercelAICommit
	zodVersion        = "4.6.7"
)

// The COMPAT.md sections the upstream tables are published under.
const (
	SectionAgents = "agents-sdk"
	SectionMCP    = "mcp"
	SectionVercel = "vercel-ai"
)

const agentsPin = `"agents": "*" (a pnpm workspace dependency) is pinned to the published ` + AgentsVersion + `, the version the repo carries at this commit; npm install (the example has no lockfile of its own)`

// agentsUp is a cloudflare/agents example published in the #325 tables.
func agentsUp(id, dir string, f func(*Project)) *Project {
	p := agentsEx(id, dir)
	p.Section = SectionAgents
	p.Pins = map[string]string{"agents": AgentsVersion}
	p.Install = InstallUpgrade
	p.Workarounds = []string{agentsPin}
	f(&p)
	return &p
}

// viteBuild is the examples' `vite build` (their deploy script is
// `vite build && wrangler deploy`).
var viteBuild = [][]string{{"npx", "vite", "build"}}

// upstreamProjects are appended to Projects().
func upstreamProjects() []*Project {
	return []*Project{
		// --- cloudflare/agents examples (#325) ---
		agentsUp("ag-cross-domain", "cross-domain", func(p *Project) {
			p.What = "Agent behind onBeforeConnect / onBeforeRequest token checks: HTTP and WebSocket"
			p.Exercises = []string{"agents-sdk", "websockets"}
			p.Probe = probeCrossDomain
		}),
		agentsUp("ag-github-webhook", "github-webhook", func(p *Project) {
			p.What = "GitHub webhook receiver: HMAC check, one agent per repo, SQL event log, state sync"
			p.Exercises = []string{"agents-sdk", "do-storage", "secrets", "websockets"}
			p.Build = viteBuild
			p.Secrets = map[string]string{"GITHUB_WEBHOOK_SECRET": webhookSecret}
			p.Probe = probeGitHubWebhook
		}),
		agentsUp("ag-mcp-client", "mcp-client", func(p *Project) {
			p.What = "Agent as MCP client (addMcpServer, @callable RPC)"
			p.Exercises = []string{"agents-sdk", "mcp", "websockets", "assets"}
			p.AddDeps = map[string]string{"@modelcontextprotocol/server": MCPServerVersion}
			p.Workarounds = append(p.Workarounds, mcpPeer)
			p.Build = viteBuild
			p.Probe = probeMCPClient
		}),
		agentsUp("ag-mcp-elicitation", "mcp-elicitation", func(p *Project) {
			p.What = "Stateful MCP server with elicitation (MCP SDK v1 on a Durable Object, WorkerTransport)"
			p.Exercises = []string{"agents-sdk", "mcp", "do-storage"}
			p.AddDeps = map[string]string{"@modelcontextprotocol/server": MCPServerVersion}
			p.Workarounds = append(p.Workarounds, mcpPeer)
			p.Probe = probeElicitation
		}),
		agentsUp("ag-mcp-elicitation-mrtr", "mcp-elicitation-mrtr", func(p *Project) {
			p.What = "Stateless MCP elicitation, multi-round-trip requests (MCP 2026-07-28 only)"
			p.Exercises = []string{"mcp", "secrets"}
			p.Secrets = map[string]string{"MRTR_REQUEST_STATE_KEY": "compat-mrtr-request-state-key-0123456789abcdef"}
			p.Probe = probeMRTR
		}),
		agentsUp("ag-mcp-worker", "mcp-worker", func(p *Project) {
			p.What = "Stateless MCP server in a Worker (createMcpHandler) + React tool tester"
			p.Exercises = []string{"mcp", "assets"}
			p.Build = viteBuild
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "Hello MCP Server" {
					return fmt.Errorf("serverInfo.name %q", name)
				}
				if out, err := m.Tool("hello", map[string]any{"name": "tailnet"}); err != nil || out != "Hello, tailnet!" {
					return fmt.Errorf("hello: %q %v", out, err)
				}
				return r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html")
			}
		}),
		agentsUp("ag-mcp-worker-auth", "mcp-worker-authenticated", func(p *Project) {
			p.What = "MCP server behind OAuth 2.1 (@cloudflare/workers-oauth-provider on KV)"
			p.Exercises = []string{"mcp", "kv", "vars", "assets"}
			p.Build = viteBuild
			p.OriginVar = "MCP_RESOURCE"
			// workers-oauth-provider accepts only an HTTPS resource URL
			// (`http` on loopback): deployed, the workload is called at
			// its https:// tailnet URL (#360).
			p.TailnetHTTPS = true
			p.Changes = []string{
				"`vars.MCP_RESOURCE` in wrangler.jsonc set to the served URL + `/mcp`, as the example's README says to",
				"deployed: `\"tailnet\": {\"https\": true}` in tailworks.jsonc, so the resource URL is the workload's `https://` tailnet URL ([#360](https://github.com/rosskukulinski/tailworks/issues/360))",
			}
			p.Probe = probeMCPWorkerAuth
		}),
		agentsUp("ag-push-notifications", "push-notifications", func(p *Project) {
			p.What = "Reminder agent: @callable RPC, schedule(), state sync, Web Push (VAPID secrets)"
			p.Exercises = []string{"agents-sdk", "secrets", "websockets", "assets", "nodejs-compat"}
			p.Build = viteBuild
			p.Secrets = vapidSecrets()
			p.Probe = probePushNotifications
		}),
		agentsUp("ag-webmcp", "webmcp", func(p *Project) {
			p.What = "McpAgent (MCP SDK v1, state in a Durable Object) + browser WebMCP adapter page"
			p.Exercises = []string{"agents-sdk", "mcp", "do-storage", "assets"}
			p.AddDeps = map[string]string{
				"@cloudflare/vite-plugin":      "1.62.3",
				"@modelcontextprotocol/server": MCPServerVersion,
				"@vitejs/plugin-react":         "^6.0.3",
				"react":                        "^19.2.7",
				"react-dom":                    "^19.2.7",
				"vite":                         "^8.1.6",
			}
			p.Workarounds = append(p.Workarounds, mcpPeer, "the build tools its package.json leaves to the monorepo root are added at the root's versions: @cloudflare/vite-plugin, @vitejs/plugin-react, vite, react, react-dom")
			p.Build = viteBuild
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "WebMCP Demo" {
					return fmt.Errorf("serverInfo.name %q", name)
				}
				if out, err := m.Tool("add", map[string]any{"a": 41}); err != nil || out != "Added 41, total is now 41" {
					return fmt.Errorf("add 41: %q %v", out, err)
				}
				if out, err := m.Tool("get_counter", map[string]any{}); err != nil || out != "Counter is currently 41" {
					return fmt.Errorf("get_counter: %q %v", out, err)
				}
				if out, err := m.Tool("greet", map[string]any{"name": "tailnet"}); err != nil || !strings.Contains(out, "Hello, tailnet!") {
					return fmt.Errorf("greet: %q %v", out, err)
				}
				return r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html")
			}
		}),
		agentsUp("ag-webmcp-react", "webmcp-react", func(p *Project) {
			p.What = "WebMCP starter: a React SPA (tools run in the browser), Worker only as the 404 fallback"
			p.Exercises = []string{"assets"}
			p.Build = viteBuild
			// Needs npm 11 (Node 24, which the example's "engines" asks for):
			// npm 10 crashes resolving its peers ("Cannot read properties of
			// null (reading 'edgesOut')"), and --legacy-peer-deps leaves out
			// peers the build needs. The nightly job runs on Node 24.
			p.Probe = func(r *Run) error {
				res := r.Get("/", "sec-fetch-mode", "navigate")
				if err := res.Want(200, "<html"); err != nil {
					return err
				}
				m := assetRe.FindStringSubmatch(res.Body)
				if m == nil {
					return fmt.Errorf("no /assets/*.js in the page")
				}
				if err := r.Get(m[0]).Want(200); err != nil {
					return err
				}
				// not_found_handling: single-page-application.
				return r.Get("/some/client/route", "sec-fetch-mode", "navigate").Want(200, "<html")
			}
		}),
		agentsUp("ag-workflows", "workflows", func(p *Project) {
			p.What = "AgentWorkflow started by an agent: steps, progress reports to the agent and its clients, restart, list. (Its approval step sits behind the example's intentional random failures and isn't reached)"
			p.Exercises = []string{"agents-sdk", "workflows", "websockets", "assets"}
			p.Build = viteBuild
			p.Probe = probeAgentsWorkflows
		}),
		agentsUp("ag-playground", "playground", func(p *Project) {
			p.What = "The Agents playground: 30 agent classes in one Worker (state, callable, streaming, schedule, SQL, routing, multi-agent, MCP, workflows; AI, email, voice and codemode demos too)"
			p.Exercises = []string{"agents-sdk", "mcp", "workflows", "websockets", "do-storage", "assets"}
			p.Pins = map[string]string{"agents": AgentsVersion, "@cloudflare/ai-chat": "0.12.3", "@cloudflare/codemode": "0.5.6"}
			p.AddDeps = map[string]string{"@ai-sdk/react": "^4.0.0"}
			p.Workarounds = []string{agentsPin + `; likewise "@cloudflare/ai-chat" 0.12.3 and "@cloudflare/codemode" 0.5.6; @ai-sdk/react (a peer the monorepo root provides) added at the root's ^4.0.0`}
			p.Build = viteBuild
			p.Probe = probePlayground
		}),

		// --- vercel/mcp-handler examples (#325) ---
		mcpHandlerEx("mcph-route", "examples", "route.ts", func(p *Project) {
			p.What = "`createMcpHandler`, one tool"
			p.Probe = func(r *Run) error {
				m, _, err := r.MCPConnect(mcphRoute)
				if err != nil {
					return err
				}
				if out, err := m.Tool("echo", map[string]any{}); err != nil || out != "Hello, world!" {
					return fmt.Errorf("echo: %q %v", out, err)
				}
				return nil
			}
		}),
		mcpHandlerEx("mcph-auth", "examples/auth", "route.ts", func(p *Project) {
			p.What = "`withMcpAuth`: bearer token with required scopes"
			p.Probe = func(r *Run) error {
				res := r.PostJSON(mcphRoute, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, "accept", "application/json, text/event-stream")
				if res.Code != 401 || !strings.Contains(res.Header.Get("www-authenticate"), "resource_metadata") {
					return fmt.Errorf("without a token: want 401 with a resource_metadata challenge, got %s %q", res, res.Header.Get("www-authenticate"))
				}
				r.Hdr = []string{"authorization", "Bearer __TEST_VALUE__compat"}
				m, _, err := r.MCPConnect(mcphRoute)
				if err != nil {
					return err
				}
				if out, err := m.Tool("echo", map[string]any{"message": "hi"}); err != nil || out != "Echo: hi (from example-client)" {
					return fmt.Errorf("echo: %q %v", out, err)
				}
				return nil
			}
		}),
		mcpHandlerEx("mcph-auth-cookie", "examples/auth-cookie", "route.ts", func(p *Project) {
			p.What = "`withMcpAuth`: same-origin session cookie, or bearer token"
			p.Probe = func(r *Run) error {
				res := r.PostJSON(mcphRoute, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, "accept", "application/json, text/event-stream")
				if res.Code != 401 {
					return fmt.Errorf("without a session: want 401, got %s", res)
				}
				// A cross-site cookie is refused; a same-origin one is the user.
				r.Hdr = []string{"cookie", "session=__TEST_SESSION__compat", "sec-fetch-site", "cross-site"}
				if res := r.PostJSON(mcphRoute, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, "accept", "application/json, text/event-stream"); res.Code != 401 {
					return fmt.Errorf("cross-site cookie: want 401, got %s", res)
				}
				r.Hdr = []string{"cookie", "session=__TEST_SESSION__compat", "sec-fetch-site", "same-origin"}
				m, _, err := r.MCPConnect(mcphRoute)
				if err != nil {
					return err
				}
				if out, err := m.Tool("echo", map[string]any{"message": "hi"}); err != nil || out != "Echo: hi for user user123" {
					return fmt.Errorf("echo: %q %v", out, err)
				}
				return nil
			}
		}),

		// --- Vercel AI SDK examples (#325) ---
		with(Project{ID: "ai-hono", Repo: "vercel/ai", Commit: vercelAICommit, Dir: "examples/hono"}, func(p *Project) {
			p.Section = SectionVercel
			p.What = "Hono server: `streamText`, text and UI message streams, a `ToolLoopAgent` behind `createAgentUIStreamResponse`"
			p.Exercises = []string{"vercel-ai", "nodejs-compat"}
			p.Pins = map[string]string{"ai": AIVersion, "@ai-sdk/openai": AIOpenAIVersion}
			p.Install = InstallUpgrade
			p.Files = map[string]string{
				"wrangler.jsonc":           honoWrangler,
				"tw-compat-entry.ts":       honoEntry,
				"tw-compat-node-server.ts": honoServeShim,
				"tw-compat-openai.ts":      openAIStub,
			}
			p.Secrets = map[string]string{"OPENAI_API_KEY": "tw-compat-no-key"}
			p.Workarounds = []string{aiPin}
			p.Changes = []string{
				"the example is a Node server with no Workers config: a wrangler.jsonc and a Worker entry are added, and `@hono/node-server` is aliased to a shim whose `serve()` hands Hono's `app.fetch` to that entry instead of listening on port 8080; `src/` is unchanged",
				noKeyChange,
			}
			p.Probe = probeAIHono
		}),
		nodeHTTPEx("ai-node-http", "examples/node-http-server", "", func(p *Project) {
			p.What = "`node:http` server: `streamText` piped with `pipeUIMessageStreamToResponse`"
			p.Probe = func(r *Run) error {
				if err := r.Do("POST", "/", nil).Want(200, `"type":"text-delta"`, stubText); err != nil {
					return err
				}
				return r.Do("POST", "/stream-data", nil).Want(200, `"type":"data-custom"`, "Hello, world!", stubText)
			}
		}),
		nodeHTTPEx("ai-express", "examples/express", ".js", func(p *Project) {
			p.What = "Express 5 server: `streamText`, and a `ToolLoopAgent` piped with `pipeAgentUIStreamToResponse`"
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<form"); err != nil {
					return err
				}
				if err := r.Do("POST", "/", nil).Want(200, `"type":"text-delta"`, stubText); err != nil {
					return err
				}
				if err := r.Do("POST", "/custom-data-parts", nil).Want(200, stubText); err != nil {
					return err
				}
				return r.PostJSON("/chat", chatMessages).Want(200, `"type":"text-delta"`, stubText)
			}
		}),
	}
}

const mcpPeer = "@modelcontextprotocol/server@" + MCPServerVersion + " added: a peer of `agents` that the monorepo hoists"

// --- vercel/mcp-handler: route modules, mounted by a Worker entry ---

const mcphRoute = "/api/mcp"

func mcpHandlerEx(id, dir, route string, f func(*Project)) *Project {
	p := Project{ID: id, Repo: "vercel/mcp-handler", Commit: mcpHandlerCommit, Dir: dir, Section: SectionMCP}
	p.Exercises = []string{"mcp", "nodejs-compat"}
	pkg, _ := json.MarshalIndent(map[string]any{
		"name": id, "private": true, "type": "module",
		"dependencies": map[string]string{"mcp-handler": McpHandlerVersion, "@modelcontextprotocol/server": MCPServerVersion, "zod": zodVersion},
	}, "", "  ")
	p.Files = map[string]string{
		"package.json":   string(pkg) + "\n",
		"wrangler.jsonc": "// Written by e2e/compat.\n{\n  \"name\": \"" + id + "\",\n  \"main\": \"tw-compat-entry.ts\",\n  \"compatibility_date\": \"2026-06-11\",\n  \"compatibility_flags\": [\"nodejs_compat\"]\n}\n",
		"tw-compat-entry.ts": `// Written by e2e/compat: mounts the example's route module at ` + mcphRoute + `
// (the README's route), the way a Next.js route handler is mounted.
import * as route from "./` + strings.TrimSuffix(route, ".ts") + `";

const handlers = route as unknown as Record<string, (req: Request) => Response | Promise<Response>>;

export default {
  fetch(req: Request): Response | Promise<Response> {
    if (new URL(req.url).pathname !== "` + mcphRoute + `") return new Response("Not found", { status: 404 });
    const h = handlers[req.method];
    return h ? h(req) : new Response("Method not allowed", { status: 405 });
  },
};
`,
	}
	p.Workarounds = []string{"mcp-handler@" + McpHandlerVersion + ", @modelcontextprotocol/server@" + MCPServerVersion + " and zod@" + zodVersion + " installed as the README says (`npm install mcp-handler@^2 @modelcontextprotocol/server@^2 zod@^4`), pinned"}
	p.Changes = []string{"the example is a route module (`export { handler as GET, handler as POST }`), not a project: a package.json, a wrangler.jsonc and a 10-line Worker entry that mounts it at `" + mcphRoute + "` are added; `route.ts` is unchanged"}
	f(&p)
	return &p
}

// --- The Hono example (Vercel AI SDK): a Node server, run as a Worker ---

// honoWrangler aliases @hono/node-server, which the example serves its
// Hono app with (Node's http server, and it replaces the global Request
// and Response, which no Workers runtime survives: stock `wrangler dev`
// fails the same way), to honoServeShim.
const honoWrangler = `// Written by e2e/compat.
{
  "name": "ai-hono",
  "main": "tw-compat-entry.ts",
  "compatibility_date": "2026-06-11",
  "compatibility_flags": ["nodejs_compat"],
  "alias": { "@hono/node-server": "./tw-compat-node-server.ts" }
}
`

const honoServeShim = `// Written by e2e/compat: stands in for @hono/node-server. serve() keeps the
// app's fetch handler for the Worker entry instead of listening on a port.
type Fetch = (req: Request, env?: unknown, ctx?: unknown) => Response | Promise<Response>;
let served: Fetch | undefined;

export function serve(options: { fetch: Fetch; port?: number }): void {
  served = options.fetch;
}

export function handler(): Fetch {
  if (!served) throw new Error("the example did not call serve()");
  return served;
}
`

const honoEntry = `// Written by e2e/compat: the Worker entry. The first import answers the
// example's OpenAI calls with a scripted stream; the second runs the
// example's server module, unchanged, whose serve() call hands over app.fetch.
import "./tw-compat-openai";
import "./src/server";
import { handler } from "./tw-compat-node-server";

export default {
  fetch(req: Request, env: unknown, ctx: unknown): Response | Promise<Response> {
    return handler()(req, env, ctx);
  },
};
`

// --- node:http examples (Vercel AI SDK): served by Cloudflare's bridge ---

// nodeHTTPEx is an AI SDK example that is a Node server listening on port
// 8080 (node:http, Express). It runs as a Worker through Cloudflare's own
// node:http bridge, `httpServerHandler` from `cloudflare:node`.
func nodeHTTPEx(id, dir, ext string, f func(*Project)) *Project {
	p := Project{ID: id, Repo: "vercel/ai", Commit: vercelAICommit, Dir: dir, Section: SectionVercel}
	p.Exercises = []string{"vercel-ai", "nodejs-compat"}
	p.Pins = map[string]string{"ai": AIVersion, "@ai-sdk/openai": AIOpenAIVersion}
	p.Install = InstallUpgrade
	p.Files = map[string]string{
		"wrangler.jsonc": "// Written by e2e/compat.\n{\n  \"name\": \"" + id + "\",\n  \"main\": \"tw-compat-entry.ts\",\n  \"compatibility_date\": \"2026-06-11\",\n  \"compatibility_flags\": [\"nodejs_compat\"]\n}\n",
		"tw-compat-entry.ts": `// Written by e2e/compat: the Worker entry. The first import answers the
// example's OpenAI calls with a scripted stream; the second runs the
// example's server module, unchanged (it listens on port 8080); the export
// is Cloudflare's node:http bridge to that port.
import "./tw-compat-openai";
import "./src/server` + ext + `";
import { httpServerHandler } from "cloudflare:node";

export default httpServerHandler({ port: 8080 });
`,
		"tw-compat-openai.ts": openAIStub,
	}
	p.Secrets = map[string]string{"OPENAI_API_KEY": "tw-compat-no-key"}
	p.Workarounds = []string{aiPin}
	p.Changes = []string{
		"the example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: 8080 })` from `cloudflare:node`) are added; `src/` is unchanged",
		noKeyChange,
	}
	f(&p)
	return &p
}

const (
	aiPin       = `"ai" and "@ai-sdk/openai" ("workspace:*") are pinned to the published ` + AIVersion + ` and ` + AIOpenAIVersion + `, the versions the repo carries at this commit; npm install`
	noKeyChange = "no OpenAI key: the entry answers the example's OpenAI Responses API calls with a scripted stream, so no model is called"
	stubText    = "tw-compat scripted reply"
)

// chatMessages is what useChat posts.
var chatMessages = map[string]any{"messages": []any{map[string]any{"id": "m1", "role": "user", "parts": []any{map[string]any{"type": "text", "text": "hello"}}}}}

// openAIStub answers POST <base>/responses (the OpenAI Responses API, as
// @ai-sdk/openai calls it) with a scripted SSE stream: a function call to
// the first function tool on the first turn when there is one, else text.
// Every other fetch goes out unchanged.
const openAIStub = `// Written by e2e/compat: a scripted OpenAI Responses API. No key, no network.
// Plain JavaScript (no type annotations), so it is also a .js file.
const realFetch = globalThis.fetch;
export const STUB_TEXT = "tw-compat scripted reply";

function sse(events) {
  const body = events.map((e) => "data: " + JSON.stringify(e) + "\n\n").join("");
  return new Response(body, { headers: { "content-type": "text/event-stream" } });
}

function reply(req) {
  const model = req.model ?? "stub";
  const base = { id: "resp_twcompat", object: "response", created_at: 1, model, status: "in_progress", output: [] };
  const usage = { input_tokens: 1, output_tokens: 1, total_tokens: 2, input_tokens_details: { cached_tokens: 0 }, output_tokens_details: { reasoning_tokens: 0 } };
  const input = Array.isArray(req.input) ? req.input : [];
  const fn = (req.tools ?? []).find((t) => t.type === "function");
  const called = input.some((i) => i.type === "function_call_output");
  if (fn && !called) {
    const item = { type: "function_call", id: "fc_twcompat", call_id: "call_twcompat", name: fn.name, arguments: JSON.stringify({ city: "Tailnet" }), status: "completed" };
    return sse([
      { type: "response.created", response: base },
      { type: "response.output_item.added", output_index: 0, item: { ...item, arguments: "", status: "in_progress" } },
      { type: "response.function_call_arguments.delta", item_id: item.id, output_index: 0, delta: item.arguments },
      { type: "response.output_item.done", output_index: 0, item },
      { type: "response.completed", response: { ...base, status: "completed", output: [item], usage } },
    ]);
  }
  const msg = { type: "message", id: "msg_twcompat", role: "assistant", status: "completed", content: [{ type: "output_text", text: STUB_TEXT, annotations: [] }] };
  return sse([
    { type: "response.created", response: base },
    { type: "response.output_item.added", output_index: 0, item: { ...msg, status: "in_progress", content: [] } },
    { type: "response.content_part.added", item_id: msg.id, output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
    { type: "response.output_text.delta", item_id: msg.id, output_index: 0, content_index: 0, delta: STUB_TEXT },
    { type: "response.output_text.done", item_id: msg.id, output_index: 0, content_index: 0, text: STUB_TEXT },
    { type: "response.content_part.done", item_id: msg.id, output_index: 0, content_index: 0, part: msg.content[0] },
    { type: "response.output_item.done", output_index: 0, item: msg },
    { type: "response.completed", response: { ...base, status: "completed", output: [msg], usage } },
  ]);
}

globalThis.fetch = async (input, init) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  if (new URL(url).pathname.endsWith("/responses")) {
    const body = init?.body ?? (input instanceof Request ? await input.clone().text() : "{}");
    return reply(JSON.parse(String(body)));
  }
  return realFetch(input, init);
};
`

func probeAIHono(r *Run) error {
	if err := r.Get("/health").Want(200, "Hono AI SDK example server is running!"); err != nil {
		return err
	}
	// streamText → text stream.
	if err := r.Do("POST", "/text", nil).Want(200, "tw-compat scripted reply"); err != nil {
		return err
	}
	// streamText → UI message stream (SSE).
	res := r.Do("POST", "/", nil)
	if err := res.Want(200, `"type":"text-delta"`, "tw-compat scripted reply"); err != nil {
		return err
	}
	if ct := res.Header.Get("content-type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("POST /: content-type %q", ct)
	}
	// createUIMessageStream: custom data, then the merged model stream.
	if err := r.Do("POST", "/stream-data", nil).Want(200, `"type":"data-custom"`, "Hello, world!", "tw-compat scripted reply"); err != nil {
		return err
	}
	// ToolLoopAgent through createAgentUIStreamResponse (what useChat posts).
	msgs := map[string]any{"messages": []any{map[string]any{"id": "m1", "role": "user", "parts": []any{map[string]any{"type": "text", "text": "hello"}}}}}
	return r.PostJSON("/chat", msgs).Want(200, `"type":"text-delta"`, "tw-compat scripted reply")
}

// --- cloudflare/agents probes ---

func probeCrossDomain(r *Run) error {
	const path = "/agents/my-agent/compat"
	if err := r.Get(path).Want(401, "Unauthorized"); err != nil {
		return err
	}
	if err := r.Get(path, "authorization", "Bearer demo-token-123").Want(200, "Authenticated HTTP request processed"); err != nil {
		return err
	}
	if _, err := r.WS(path + "?token=nope"); err == nil {
		return fmt.Errorf("a WebSocket with a bad token connected")
	}
	a, err := r.Agent(path + "?token=demo-token-123&userId=compat")
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err := a.Wait(10*time.Second, func(m string) bool { return strings.Contains(m, "Welcome compat!") }); err != nil {
		return err
	}
	if err := WSWrite(a.c, "ping from the tailnet"); err != nil {
		return err
	}
	_, err = a.Wait(10*time.Second, func(m string) bool { return strings.Contains(m, `Server received "ping from the tailnet"`) })
	return err
}

const webhookSecret = "compat-webhook-secret"

func probeGitHubWebhook(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	var an struct{ AgentName string }
	if err := r.Get("/api/agent-name?repo=tailworks/compat").JSON(&an); err != nil || an.AgentName == "" {
		return fmt.Errorf("agent-name: %v %+v", err, an)
	}
	a, err := r.Agent("/agents/repo-agent/" + an.AgentName)
	if err != nil {
		return err
	}
	defer a.Close()
	body, _ := json.Marshal(map[string]any{
		"zen":        "Keep it logically awesome.",
		"repository": map[string]any{"full_name": "tailworks/compat", "html_url": "https://example.com/r", "stargazers_count": 7, "forks_count": 3, "open_issues_count": 2},
		"sender":     map[string]any{"login": "compat", "avatar_url": ""},
	})
	if res := r.Do("POST", "/webhooks/github/tailworks/compat", body, "content-type", "application/json", "x-github-event", "ping", "x-hub-signature-256", "sha256=00"); res.Code != 401 {
		return fmt.Errorf("a bad signature: want 401, got %s", res)
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if err := r.Do("POST", "/webhooks/github/tailworks/compat", body, "content-type", "application/json", "x-github-event", "ping", "x-hub-signature-256", sig).Want(200, "OK"); err != nil {
		return err
	}
	// The agent's state reaches the connected client (cf_agent_state).
	if _, err := a.Wait(15*time.Second, func(m string) bool {
		return strings.Contains(m, "cf_agent_state") && strings.Contains(m, `"stars":7`) && strings.Contains(m, `"webhookConfigured":true`)
	}); err != nil {
		return err
	}
	// The event is in the agent's SQLite table.
	out, err := a.RPC("getEvents", 10)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "Webhook configured") {
		return fmt.Errorf("getEvents: %s", tail(string(out), 300))
	}
	return nil
}

func probeMCPClient(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	a, err := r.Agent("/agents/my-agent/compat")
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err := a.Wait(10*time.Second, func(m string) bool { return strings.Contains(m, "cf_agent_mcp_servers") }); err != nil {
		return err
	}
	// addMcpServer to a URL nothing answers on: the call returns, and the
	// failure is the server's state (the example shows it in the UI).
	if _, err := a.RPC("addServer", "nowhere", "http://mcp.invalid/mcp"); err != nil {
		r.Note("addServer(unreachable) → %v", err)
	}
	if _, err := a.Wait(30*time.Second, func(m string) bool {
		return strings.Contains(m, "cf_agent_mcp_servers") && strings.Contains(m, "nowhere")
	}); err != nil {
		return err
	}
	r.Note("not exercised: a reachable MCP server (the run's tailnet policy lets the workload reach no peer; e2e/sdk AgentAsMCPClient covers it)")
	return nil
}

func probePushNotifications(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	a, err := r.Agent("/agents/reminder-agent/compat")
	if err != nil {
		return err
	}
	defer a.Close()
	out, err := a.RPC("getVapidPublicKey")
	if err != nil {
		return err
	}
	var key string
	if json.Unmarshal(out, &key); len(key) < 80 {
		return fmt.Errorf("getVapidPublicKey: %s (the VAPID_PUBLIC_KEY secret)", out)
	}
	// A subscription whose push service nothing answers on: the send fails
	// in web-push (VAPID signing with node:crypto runs first), not before.
	sub := map[string]any{"endpoint": "https://push.invalid/send/compat", "expirationTime": nil,
		"keys": map[string]any{"p256dh": vapidPair().pub, "auth": base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))}}
	if out, err := a.RPC("subscribe", sub); err != nil || !strings.Contains(string(out), `"ok":true`) {
		return fmt.Errorf("subscribe: %s %v", out, err)
	}
	out, err = a.RPC("sendTestNotification")
	if err != nil {
		return err
	}
	r.Note("sendTestNotification → %s (the push service is not reachable from the run)", out)
	if !strings.Contains(string(out), `"failed":1`) {
		return fmt.Errorf("sendTestNotification: %s", out)
	}
	// A reminder in 2 s: schedule() fires sendReminder, which broadcasts.
	if _, err := a.RPC("createReminder", "compat reminder", 2); err != nil {
		return err
	}
	_, err = a.Wait(40*time.Second, func(m string) bool { return strings.Contains(m, "reminder_sent") })
	return err
}

type vapid struct{ pub, priv string }

var vapidOnce *vapid

// vapidPair is a VAPID key pair for the run (the example's
// `npm run generate-vapid-keys`): P-256, public key as the raw point,
// private key as d, both base64url.
func vapidPair() *vapid {
	if vapidOnce == nil {
		k, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		vapidOnce = &vapid{pub: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), priv: base64.RawURLEncoding.EncodeToString(k.Bytes())}
	}
	return vapidOnce
}

func vapidSecrets() map[string]string {
	v := vapidPair()
	return map[string]string{"VAPID_PUBLIC_KEY": v.pub, "VAPID_PRIVATE_KEY": v.priv, "VAPID_SUBJECT": "mailto:compat@example.com"}
}

// NotRun is an upstream example the suite doesn't run, and why.
type NotRun struct {
	Section string
	Repo    string
	Commit  string
	// Dirs are the example directories, relative to Base.
	Base string
	Dirs []string
	Why  string
}

// UpstreamNotRun lists every example of the upstream projects that has no
// Project, so the published tables account for all of them.
func UpstreamNotRun() []NotRun {
	ag := func(why string, dirs ...string) NotRun {
		return NotRun{Section: SectionAgents, Repo: "cloudflare/agents", Commit: agentsCommit, Base: "examples/", Dirs: dirs, Why: why}
	}
	ai := func(why string, dirs ...string) NotRun {
		return NotRun{Section: SectionVercel, Repo: "vercel/ai", Commit: vercelAICommit, Base: "examples/", Dirs: dirs, Why: why}
	}
	return append(pythonNotRun(), []NotRun{ // python.go
		ag("needs Workers AI (`env.AI`, an `ai` binding in its wrangler config), which tailworks doesn't provide. A scripted `env.AI` isn't possible without changing each example: tailworks ignores the `ai` binding (`env.AI` is undefined), so there is no binding to point at a stub, and one would have to be wired into each example: its config, or a wrapper around each of its agent classes (they read `this.env.AI` inside their Durable Objects)",
			"a2a", "agents-as-tools", "auth-agent", "chat-sdk-messenger", "codemode-browser", "context-overflow-recovery", "deploy-churn",
			"dynamic-tools", "mcp-rpc-transport", "multi-ai-chat", "resumable-stream-chat", "structured-input", "think-chat-sdk",
			"think-submissions", "think-workflows", "vue-chat", "web-fetch-lab", "web-research"),
		ag("needs Workers AI, and a voice or telephony provider's API key",
			"elevenlabs-starter", "plivo-voice-agent", "telnyx-voice-agent", "voice-agent", "voice-input"),
		ag("needs Workers AI and Browser Rendering (`browser` binding)",
			"ai-chat", "browser-live-view", "browser-quick-actions", "codemode-connectors"),
		ag("needs Workers AI and the Worker Loader (`worker_loaders` binding)",
			"codemode", "computer-skills", "r2-skills", "worker-bundler-playground", "workspace-chat"),
		ag("needs the Worker Loader (`worker_loaders` binding), which tailworks keeps to the platform",
			"codemode-mcp", "codemode-mcp-openapi", "dynamic-workers", "dynamic-workers-playground"),
		ag("needs Workers AI and a container (`containers` binding with a Dockerfile); the suite runs the process tier only",
			"sandbox-coding-agent"),
		ag("needs Email Routing (`send_email` binding, the `email()` handler)",
			"email-agent"),
		ag("needs an OpenAI API key (the agent's moves come from `@ai-sdk/openai`)",
			"tictactoe"),
		ag("needs a funded test wallet key and the public x402.org facilitator",
			"x402", "x402-mcp"),
		ag("needs Workers AI, the Worker Loader, a container or a model provider key",
			"next/channels", "next/dynamic-agents", "next/harnesses/*", "next/models", "next/tools/browser"),

		ai("a Next.js, Nuxt, SvelteKit or Angular app that calls a model provider with an API key. tailworks' Worker tier runs Workers projects, not these frameworks' builds, and none of them has a Workers target",
			"ai-e2e-next", "angular", "harness-e2e-next", "next", "next-agent", "next-fastapi", "next-google-vertex", "next-langchain",
			"next-openai-kasada-bot-protection", "next-openai-pages", "next-openai-telemetry", "next-openai-telemetry-sentry",
			"next-openai-upstash-rate-limits", "next-workflow", "nuxt-openai", "sveltekit-openai", "xai-tts-demo"),
		ai("scripts and a terminal UI, not servers; they call model providers with API keys",
			"ai-functions", "harness-e2e-tui"),
	}...)
}
