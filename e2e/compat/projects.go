package compat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The pinned upstream commits. Bumping one re-runs every project from that
// repo against the new code (the prepared copies are keyed by commit).
const (
	templatesCommit = "9674298a122ac464f8d4d8d2b38c3491d6b35d18" // cloudflare/templates main, 2026-11-23
	workersSDK      = "de6b171603576fd12afa4f32736c0c0ecb28eeab" // cloudflare/workers-sdk main, 2026-11-23
	agentsCommit    = "95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c" // cloudflare/agents main, 2026-11-23
	aiCommit        = "34a8f4c00c0871883215a7114e9a7f3e7747ee1b" // cloudflare/ai main, 2026-11-23
)

const c3Dir = "packages/create-cloudflare/templates/"

func tpl(id, dir string) Project {
	return Project{ID: id, Repo: "cloudflare/templates", Commit: templatesCommit, Dir: dir}
}

func c3(id, dir string) Project {
	return Project{ID: id, Repo: "cloudflare/workers-sdk", Commit: workersSDK, Dir: c3Dir + dir, C3: true}
}

func agentsEx(id, dir string) Project {
	return Project{ID: id, Repo: "cloudflare/agents", Commit: agentsCommit, Dir: "examples/" + dir}
}

func aiDemo(id, dir string) Project {
	return Project{ID: id, Repo: "cloudflare/ai", Commit: aiCommit, Dir: "demos/" + dir}
}

func with(p Project, f func(*Project)) *Project { f(&p); return &p }

var npmBuild = [][]string{{"npm", "run", "build"}}

// redirectWorkaround: Vite plugin projects. wrangler deploy follows the
// build's .wrangler/deploy/config.json to the generated config
// (dist/<worker>/wrangler.json: no_bundle, ESModule rules over the
// pre-split chunks); tw deploy reads the project's own config instead.
const redirectWorkaround = "deploys the Vite plugin's output directory with --wrangler: tw deploy doesn't follow .wrangler/deploy/config.json, and its built-in bundler refuses the generated config's ESModule rules"

// Projects is the suite. Order is the run order (cheap ones first).
func Projects() []*Project {
	ps := append(cloudflareProjects(), pythonDeployedOnly()...) // python.go
	ps = append(ps, upstreamProjects()...)
	ps = append(ps, upstreamMoreProjects()...) // upstream_more.go
	return append(ps, pythonProjects()...)
}

// cloudflareProjects are the example projects of issue #305; upstream.go
// has the upstream examples of issue #325.
func cloudflareProjects() []*Project {
	return []*Project{
		// --- create-cloudflare (npm create cloudflare) templates ---
		with(c3("c3-hello-world", "hello-world/ts"), func(p *Project) {
			p.What = "C3 \"Worker only\" (TypeScript)"
			p.Exercises = []string{"fetch"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "Hello World!") }
		}),
		with(c3("c3-hello-world-js", "hello-world/js"), func(p *Project) {
			p.What = "C3 \"Worker only\" (JavaScript)"
			p.Exercises = []string{"fetch"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "Hello World!") }
		}),
		with(c3("c3-worker-assets", "hello-world-with-assets/ts"), func(p *Project) {
			p.What = "C3 Worker + static assets"
			p.Exercises = []string{"assets", "fetch"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<title>Hello, World!"); err != nil {
					return err
				}
				if err := r.Get("/message").Want(200, "Hello, World!"); err != nil {
					return err
				}
				a, b := r.Get("/random"), r.Get("/random")
				if err := a.Want(200); err != nil {
					return err
				}
				if !uuidRe.MatchString(strings.TrimSpace(a.Body)) || a.Body == b.Body {
					return fmt.Errorf("/random: want two different UUIDs, got %q %q", a.Body, b.Body)
				}
				return r.Get("/nope").Want(404, "Not Found")
			}
		}),
		with(c3("c3-assets-only", "hello-world-assets-only/templates"), func(p *Project) {
			p.What = "C3 static site (assets, no Worker)"
			p.Exercises = []string{"assets"}
			p.Install = InstallNone
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<title>Hello, World!"); err != nil {
					return err
				}
				return r.Get("/index.html").Want(307) // assets' default html_handling: /index.html → /
			}
		}),
		with(c3("c3-durable-object", "hello-world-durable-object/ts"), func(p *Project) {
			p.What = "C3 Durable Object (RPC)"
			p.Exercises = []string{"do-rpc", "do-storage"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "Hello, world!") }
		}),
		with(c3("c3-durable-object-assets", "hello-world-durable-object-with-assets/ts"), func(p *Project) {
			p.What = "C3 Durable Object + static assets"
			p.Exercises = []string{"do-rpc", "assets"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<title>Hello, World!"); err != nil {
					return err
				}
				return r.Get("/greet").Want(200, "Hello, world!")
			}
		}),
		with(c3("c3-openapi", "openapi/ts"), func(p *Project) {
			p.What = "C3 OpenAPI (Hono + chanfana)"
			p.Exercises = []string{"fetch"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/openapi.json").Want(200, `"/api/tasks"`); err != nil {
					return err
				}
				if err := r.Get("/").Want(200, "swagger"); err != nil {
					return err
				}
				if err := r.Get("/api/tasks?page=0").Want(200, `"success":true`); err != nil {
					return err
				}
				return r.PostJSON("/api/tasks", map[string]any{"name": "x"}).Want(400) // validated by the schema
			}
		}),
		with(c3("c3-scheduled", "scheduled/ts"), func(p *Project) {
			p.What = "C3 Scheduled Worker (cron * * * * *)"
			p.Exercises = []string{"cron", "fetch"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "--test-scheduled"); err != nil {
					return err
				}
				// The cron fires every minute and fetches api.cloudflare.com.
				_, err := r.WaitLog("trigger fired at * * * * *: success", 150*time.Second)
				return err
			}
		}),
		with(c3("c3-queues", "queues/ts"), func(p *Project) {
			p.What = "C3 Queue producer + consumer"
			p.Exercises = []string{"queues", "queue-consumers"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/compat-probe").Want(200, "Sent message to the queue"); err != nil {
					return err
				}
				_, err := r.WaitLog(`/compat-probe","method":"GET"`, 90*time.Second)
				return err
			}
		}),
		with(c3("c3-workflows", "hello-world-workflows/ts"), func(p *Project) {
			p.What = "C3 Workflow (step.do, waitForEvent, sleep)"
			p.Exercises = []string{"workflows"}
			p.Probe = func(r *Run) error {
				var created struct {
					ID      string `json:"id"`
					Details struct{ Status string }
				}
				if err := r.Get("/").JSON(&created); err != nil || created.ID == "" {
					return fmt.Errorf("create: %v %+v", err, created)
				}
				// First step done, then it waits for "request-approval".
				return Eventually(60*time.Second, func() error {
					var st struct {
						Status struct {
							Status string `json:"status"`
							Output any    `json:"output"`
							Error  any    `json:"error"`
						} `json:"status"`
					}
					res := r.Get("/?instanceId=" + created.ID)
					if err := res.JSON(&st); err != nil {
						return err
					}
					if st.Status.Status != "waiting" {
						return fmt.Errorf("instance %s: want status waiting, got %s", created.ID, res)
					}
					return nil
				})
			}
		}),
		with(c3("c3-python", "hello-world/py"), func(p *Project) {
			p.What = "C3 Python Worker: a sibling module (Pyodide, issue #292)"
			p.Exercises = []string{"python"}
			p.Install = InstallNone
			p.Probe = func(r *Run) error {
				return r.Get("/").Want(200, "Hello World!")
			}
		}),

		// --- cloudflare/templates (the Workers dashboard / deploy-button templates) ---
		with(tpl("hello-world-do", "hello-world-do-template"), func(p *Project) {
			p.What = "Durable Object + SQLite storage"
			p.Exercises = []string{"do-storage", "do-rpc"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "Hello, World!"); err != nil {
					return err
				}
				return r.Get("/another/object").Want(200, "Hello, World!")
			}
		}),
		with(tpl("nodejs-http-server", "nodejs-http-server-template"), func(p *Project) {
			p.What = "node:http createServer via httpServerHandler"
			p.Exercises = []string{"nodejs-compat"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "Welcome to my Node.js app on Workers!"); err != nil {
					return err
				}
				if err := r.Get("/api/status").Want(200, `"status":"ok"`); err != nil {
					return err
				}
				return r.Get("/nope").Want(404)
			}
		}),
		with(tpl("d1-template", "d1-template"), func(p *Project) {
			p.What = "D1 + wrangler migrations"
			p.Exercises = []string{"d1", "d1-migrations"}
			p.Setup = [][]string{{"d1", "migrations", "apply", "d1-template-database"}}
			p.Probe = func(r *Run) error {
				return r.Get("/").Want(200, "Kristian", "Congrats!", "Keep up the good work!")
			}
		}),
		with(tpl("d1-sessions", "d1-starter-sessions-api-template"), func(p *Project) {
			p.What = "D1 Sessions API (withSession, bookmarks) + assets"
			p.Exercises = []string{"d1", "d1-sessions", "assets"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<html"); err != nil {
					return err
				}
				if err := r.PostJSON("/api/reset", nil).Want(200); err != nil {
					return err
				}
				w := r.PostJSON("/api/orders", map[string]any{"customerId": "c-1", "orderId": "o-1", "quantity": 3})
				if err := w.Want(200, `"o-1"`); err != nil {
					return err
				}
				bm := w.Header.Get("x-d1-bookmark")
				if bm == "" {
					return fmt.Errorf("POST /api/orders: no x-d1-bookmark header: %s", w)
				}
				r.Note("bookmark %s", bm)
				// Read in a session that continues from the write's bookmark.
				return r.Get("/api/orders", "x-d1-bookmark", bm).Want(200, `"o-1"`, `"sessionBookmark"`)
			}
		}),
		with(tpl("chanfana-openapi", "chanfana-openapi-template"), func(p *Project) {
			p.What = "OpenAPI REST API (Hono + chanfana) on D1"
			p.Exercises = []string{"d1", "d1-migrations"}
			p.Setup = [][]string{{"d1", "migrations", "apply", "openapi-template-db"}}
			p.Probe = func(r *Run) error {
				if err := r.Get("/openapi.json").Want(200, `"/tasks"`); err != nil {
					return err
				}
				task := map[string]any{"name": "compat", "slug": "compat", "description": "from e2e/compat", "completed": false, "due_date": "2026-12-01T00:00:00Z"}
				var c struct {
					Success bool
					Result  struct{ ID int }
				}
				if err := r.PostJSON("/tasks", task).JSON(&c); err != nil || !c.Success || c.Result.ID == 0 {
					return fmt.Errorf("create task: %v %+v", err, c)
				}
				if err := r.Get(fmt.Sprintf("/tasks/%d", c.Result.ID)).Want(200, `"slug":"compat"`); err != nil {
					return err
				}
				if err := r.Get("/tasks").Want(200, `"compat"`); err != nil {
					return err
				}
				if err := r.Do("DELETE", fmt.Sprintf("/tasks/%d", c.Result.ID), nil).Want(200); err != nil {
					return err
				}
				return r.Get(fmt.Sprintf("/tasks/%d", c.Result.ID)).Want(404)
			}
		}),
		with(tpl("openauth", "openauth-template"), func(p *Project) {
			p.What = "OpenAuth issuer (password provider) on KV + D1"
			p.Exercises = []string{"kv", "d1", "d1-migrations", "nodejs-compat", "logs"}
			p.Setup = [][]string{{"d1", "migrations", "apply", "openauth-template-auth-db"}}
			p.Probe = probeOpenAuth
		}),
		with(tpl("r2-explorer", "r2-explorer-template"), func(p *Project) {
			p.What = "R2 Explorer (R2 + SPA assets + secret)"
			p.Exercises = []string{"r2", "assets", "secrets"}
			p.Secrets = map[string]string{"ADMIN_PASSWORD": "compat-pass"}
			p.Probe = probeR2Explorer
		}),
		with(tpl("durable-chat", "durable-chat-template"), func(p *Project) {
			p.What = "Chat room on a hibernating Durable Object (PartyServer WebSockets)"
			p.Exercises = []string{"websockets", "hibernation", "do-storage", "assets", "wrangler-bundling"}
			// The project's wrangler.json "build" bundles the client into
			// public/dist; the built-in path runs it first (issue #495).
			p.Probe = probeDurableChat
		}),
		with(tpl("multiplayer-globe", "multiplayer-globe-template"), func(p *Project) {
			p.What = "Multiplayer globe (PartyServer WebSockets, request.cf geo)"
			p.Exercises = []string{"websockets", "assets", "wrangler-bundling"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "<html"); err != nil {
					return err
				}
				if err := r.Get("/dist/index.js").Want(200); err != nil {
					return err
				}
				c, err := r.WS("/parties/globe/default")
				if err != nil {
					return err
				}
				defer c.CloseNow()
				// Cloudflare sends this client its own marker (from
				// request.cf.latitude/longitude). Without geo in request.cf
				// the server logs a warning and sends nothing.
				if msg, err := WSRead(c, 5*time.Second); err == nil {
					r.Note("first message %q", tail(msg, 200))
				} else {
					r.Note("no marker: request.cf has no latitude/longitude on tailworks")
				}
				return nil
			}
		}),
		with(tpl("workflows-starter", "workflows-starter-template"), func(p *Project) {
			p.What = "Workflows + Durable Object status + React SPA (Vite)"
			p.Exercises = []string{"workflows", "do-storage", "websockets", "assets", "wrangler-bundling"}
			p.Build = npmBuild
			p.DeployDir = "redirect"
			p.DeployArgs = []string{"--wrangler"}
			p.Workarounds = []string{redirectWorkaround}
			p.Probe = probeWorkflowsStarter
		}),
		with(tpl("to-do-list-kv", "to-do-list-kv-template"), func(p *Project) {
			p.What = "Remix to-do list on Workers KV"
			p.Exercises = []string{"kv", "assets", "nodejs-compat"}
			p.NpmArgs = []string{"--legacy-peer-deps"}
			p.Workarounds = []string{"npm ci --legacy-peer-deps: the template's lockfile pins wrangler 4 against @remix-run/dev's optional wrangler ^3 peer, which npm 11 refuses"}
			p.Build = npmBuild
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(302); err != nil { // to a new list
					return err
				}
				list := "/compat-list"
				if err := r.PostForm(list+"?index", "intent=create&text=from+e2e%2Fcompat").Want(200); err != nil {
					if err2 := r.PostForm(list, "intent=create&text=from+e2e%2Fcompat").Want(200); err2 != nil {
						return fmt.Errorf("%v; %v", err, err2)
					}
				}
				return r.Get(list).Want(200, "from e2e/compat")
			}
		}),
		with(tpl("vite-react", "vite-react-template"), func(p *Project) {
			p.What = "React SPA + Hono API (Vite plugin build)"
			p.Exercises = []string{"assets", "fetch"}
			p.Build = npmBuild
			p.Probe = func(r *Run) error {
				if err := r.Get("/api/").Want(200, `{"name":"Cloudflare"}`); err != nil {
					return err
				}
				if err := r.Get("/").Want(200, `<div id="root">`); err != nil {
					return err
				}
				// single-page-application: a browser navigation to a client
				// route gets index.html (assets_navigation_prefers_asset_serving);
				// anything else goes to the Worker (Hono's 404).
				if err := r.Get("/some/client/route", "sec-fetch-mode", "navigate").Want(200, `<div id="root">`); err != nil {
					return err
				}
				return r.Get("/some/client/route").Want(404, "404 Not Found")
			}
		}),
		with(tpl("react-router-ssr", "react-router-starter-template"), func(p *Project) {
			p.What = "React Router v7 SSR (Vite plugin build, vars)"
			p.Exercises = []string{"vars", "assets", "nodejs-compat"}
			p.Build = npmBuild
			p.DeployDir = "redirect"
			p.DeployArgs = []string{"--wrangler"}
			p.Workarounds = []string{redirectWorkaround}
			p.Probe = func(r *Run) error {
				res := r.Get("/")
				if err := res.Want(200, "Hello from Cloudflare"); err != nil {
					return err
				}
				m := assetRe.FindStringSubmatch(res.Body)
				if m == nil {
					return fmt.Errorf("no /assets/*.js in the SSR page")
				}
				return r.Get(m[0]).Want(200)
			}
		}),
		with(tpl("astro-blog", "astro-blog-starter-template"), func(p *Project) {
			p.What = "Astro blog (@astrojs/cloudflare adapter, prerendered + assets)"
			p.Exercises = []string{"assets", "nodejs-compat"}
			p.Build = npmBuild
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "Astro Blog"); err != nil {
					return err
				}
				if err := r.Get("/blog/").Want(200, "First post"); err != nil {
					return err
				}
				if err := r.Get("/blog/first-post/").Want(200, "First post"); err != nil {
					return err
				}
				return r.Get("/rss.xml").Want(200, "<rss")
			}
		}),
		with(tpl("microfrontend", "microfrontend-template"), func(p *Project) {
			p.What = "Microfrontend router over service bindings"
			p.Exercises = []string{"service-bindings"}
			p.Expect = Expect{Kind: ExpectProbeFail, Compat: "service-bindings",
				Why: "env.APP1/APP2 are service bindings to other Workers; tailworks has none"}
			p.Probe = func(r *Run) error { return r.Get("/app1").Want(200) }
		}),
		with(tpl("text-to-image", "text-to-image-template"), func(p *Project) {
			p.What = "Workers AI text-to-image"
			p.Exercises = []string{"workers-ai"}
			p.Expect = Expect{Kind: ExpectProbeFail, Compat: "workers-ai",
				Why: "env.AI (Workers AI) isn't provided"}
			p.Probe = func(r *Run) error {
				res := r.Get("/")
				if err := res.Want(200); err != nil {
					return err
				}
				if !strings.HasPrefix(res.Body, "\x89PNG") {
					return fmt.Errorf("not a PNG: %s", res)
				}
				return nil
			}
		}),

		// --- cloudflare/ai demos ---
		with(aiDemo("remote-mcp-authless", "remote-mcp-authless"), func(p *Project) {
			p.What = "Remote MCP server, no auth (agents/mcp createMcpHandler)"
			p.Exercises = []string{"mcp", "agents-sdk", "nodejs-compat"}
			p.Workarounds = []string{"npm install (the demo is in a pnpm monorepo and has no lockfile of its own)"}
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "Authless Calculator" {
					return fmt.Errorf("serverInfo.name %q", name)
				}
				if out, err := m.Tool("add", map[string]any{"a": 2, "b": 40}); err != nil || out != "42" {
					return fmt.Errorf("add: %q %v", out, err)
				}
				if out, err := m.Tool("calculate", map[string]any{"operation": "divide", "a": 1, "b": 0}); err != nil || !strings.Contains(out, "Cannot divide by zero") {
					return fmt.Errorf("calculate divide by 0: %q %v", out, err)
				}
				return nil
			}
		}),

		// --- cloudflare/agents examples ---
		with(agentsEx("mcp-server", "mcp-server"), func(p *Project) {
			p.What = "Stateless MCP server (createMcpHandler)"
			p.Section = SectionAgents
			p.Exercises = []string{"mcp"}
			p.Workarounds = []string{"npm install (the example is in a pnpm monorepo and has no lockfile of its own)"}
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "hello-server" {
					return fmt.Errorf("serverInfo.name %q, want hello-server", name)
				}
				out, err := m.Tool("hello", map[string]any{"name": "tailnet"})
				if err != nil {
					return err
				}
				if out != "Hello, tailnet!" {
					return fmt.Errorf("hello: %q", out)
				}
				return nil
			}
		}),
		with(agentsEx("mcp-agent", "mcp"), func(p *Project) {
			p.What = "Stateful MCP server on an Agents SDK McpAgent (Durable Object state)"
			p.Exercises = []string{"agents-sdk", "mcp", "do-storage", "assets"}
			p.Pins = map[string]string{"agents": "0.34.0"}
			p.AddDeps = map[string]string{"@modelcontextprotocol/server": "2.4.0"}
			p.Install = InstallUpgrade
			p.Build = [][]string{{"npx", "vite", "build"}}
			p.Section = SectionAgents
			p.Workarounds = []string{
				`"agents": "*" (a pnpm workspace dependency) pinned to the published 0.34.0, plus its peer @modelcontextprotocol/server@2.4.0 (hoisted by the monorepo); npm install (no lockfile of its own)`,
			}
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "Demo" {
					return fmt.Errorf("serverInfo.name %q, want Demo", name)
				}
				if out, err := m.Tool("add", map[string]any{"a": 41}); err != nil || out != "Added 41, total is now 42" {
					return fmt.Errorf("add 41: %q %v", out, err)
				}
				// The counter is the McpAgent's (Durable Object) state.
				if out, err := m.Tool("add", map[string]any{"a": 1}); err != nil || out != "Added 1, total is now 43" {
					return fmt.Errorf("add 1: %q %v", out, err)
				}
				return r.Get("/").Want(200, "<html")
			}
		}),
	}
}

var (
	uuidRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	assetRe = regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.js`)
)

func probeOpenAuth(r *Run) error {
	// The issuer's metadata.
	if err := r.Get("/.well-known/oauth-authorization-server").Want(200, `"authorization_endpoint"`); err != nil {
		return err
	}
	// "/" redirects to /authorize with a client and redirect_uri …
	res := r.Get("/")
	if err := res.Want(302); err != nil {
		return err
	}
	loc := res.Header.Get("location")
	if !strings.Contains(loc, "/authorize?") || !strings.Contains(loc, "client_id=your-client-id") {
		return fmt.Errorf("/ → %q", loc)
	}
	_, path, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(loc, "http://"), "https://"), "/")
	// … which (one provider) goes to the password provider, setting the
	// authorization state cookie.
	res = r.Get("/" + path)
	if err := res.Want(302); err != nil {
		return err
	}
	cookies := cookieHeader(res)
	res = r.Get("/password/authorize", "cookie", cookies)
	if err := res.Want(200, "myAuth"); err != nil {
		return err
	}
	cookies = mergeCookies(cookies, cookieHeader(res))
	// Register: the form, then the email + password. The code goes to the
	// Worker's log (console.log); the user and password land in KV.
	res = r.Get("/password/register", "cookie", cookies)
	if err := res.Want(200, "myAuth"); err != nil {
		return err
	}
	cookies = mergeCookies(cookies, cookieHeader(res))
	email := fmt.Sprintf("compat-%d@example.com", time.Now().UnixNano())
	res = r.PostForm("/password/register", "action=register&email="+email+"&password=compat-pw-123&repeat=compat-pw-123", "cookie", cookies)
	if res.Err != nil || (res.Code != 200 && res.Code != 302) {
		return fmt.Errorf("register: %s", res)
	}
	cookies = mergeCookies(cookies, cookieHeader(res))
	line, err := r.WaitLog("Sending code ", 30*time.Second)
	if err != nil {
		return err
	}
	m := regexp.MustCompile(`Sending code (\d+) to `).FindStringSubmatch(line)
	if m == nil {
		return fmt.Errorf("no code in %q", line)
	}
	res = r.PostForm("/password/register", "action=verify&code="+m[1], "cookie", cookies)
	if err := res.Want(302); err != nil {
		return err
	}
	// The issuer redirects to the client's callback with a code, after
	// success() wrote the user to D1.
	loc = res.Header.Get("location")
	if !strings.Contains(loc, "/callback?code=") {
		return fmt.Errorf("after verify → %q", loc)
	}
	r.Note("authorization code issued (%s)", tail(loc, 80))
	return nil
}

func cookieHeader(res Res) string {
	var parts []string
	for _, c := range res.Header.Values("set-cookie") {
		kv, _, _ := strings.Cut(c, ";")
		parts = append(parts, kv)
	}
	return strings.Join(parts, "; ")
}

func mergeCookies(a, b string) string {
	m := map[string]string{}
	var order []string
	for _, s := range []string{a, b} {
		for _, kv := range strings.Split(s, "; ") {
			if kv == "" {
				continue
			}
			k, _, _ := strings.Cut(kv, "=")
			if _, ok := m[k]; !ok {
				order = append(order, k)
			}
			m[k] = kv
		}
	}
	var out []string
	for _, k := range order {
		out = append(out, m[k])
	}
	return strings.Join(out, "; ")
}

func probeR2Explorer(r *Run) error {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:compat-pass"))
	if err := r.Get("/api/server/config").Want(401); err != nil {
		return err
	}
	if err := r.Get("/api/server/config", "authorization", auth).Want(200, `"bucket"`); err != nil {
		return err
	}
	// The explorer lists the bound bucket (R2 list through the binding).
	if err := r.Get("/api/buckets/bucket", "authorization", auth).Want(200, `"objects"`); err != nil {
		return err
	}
	// Read-only (as the template configures it): uploads are refused.
	if res := r.Do("POST", "/api/buckets/bucket/upload?key=Y29tcGF0LnR4dA==", []byte("x"), "authorization", auth); res.Code < 400 {
		return fmt.Errorf("upload to a readonly explorer: %s", res)
	}
	if err := r.Get("/").Want(200, "<html"); err != nil { // the dashboard (SPA assets)
		return err
	}
	return nil
}

func probeDurableChat(r *Run) error {
	if err := r.Get("/").Want(200, "<html"); err != nil {
		return err
	}
	room := "/parties/chat/compat-room"
	c, err := r.WS(room)
	if err != nil {
		return err
	}
	first, err := WSRead(c, 10*time.Second)
	c.CloseNow()
	if err != nil || !strings.Contains(first, `"type":"all"`) {
		return fmt.Errorf("first message: %q %v", first, err)
	}
	msg := map[string]any{"type": "add", "id": fmt.Sprintf("m-%d", time.Now().UnixNano()), "content": "hello from the tailnet", "user": "compat", "role": "user"}
	b, _ := json.Marshal(msg)
	c, err = r.WS(room)
	if err != nil {
		return err
	}
	WSRead(c, 10*time.Second)
	if err := WSWrite(c, string(b)); err != nil {
		return err
	}
	if echo, err := WSRead(c, 10*time.Second); err != nil || !strings.Contains(echo, "hello from the tailnet") {
		return fmt.Errorf("broadcast: %q %v", echo, err)
	}
	c.CloseNow()
	// A new connection gets the message from SQLite storage.
	return Eventually(10*time.Second, func() error {
		c, err := r.WS(room)
		if err != nil {
			return err
		}
		defer c.CloseNow()
		all, err := WSRead(c, 10*time.Second)
		if err != nil || !strings.Contains(all, "hello from the tailnet") {
			return fmt.Errorf("history after reconnect: %q %v", all, err)
		}
		return nil
	})
}

func probeWorkflowsStarter(r *Run) error {
	if err := r.Get("/").Want(200, `<div id="root">`); err != nil {
		return err
	}
	var st struct{ InstanceID string }
	if err := r.Do("POST", "/api/workflow/start", nil).JSON(&st); err != nil || st.InstanceID == "" {
		return fmt.Errorf("start: %v %+v", err, st)
	}
	status := func() (string, error) {
		var s struct {
			Status string `json:"status"`
			Output any    `json:"output"`
		}
		res := r.Get("/api/workflow/status/" + st.InstanceID)
		if err := res.JSON(&s); err != nil {
			return "", err
		}
		return s.Status, nil
	}
	// step.do, step.sleep("2 seconds"), then waitForEvent.
	if err := Eventually(60*time.Second, func() error {
		s, err := status()
		if err == nil && s != "waiting" {
			err = fmt.Errorf("status %q, want waiting", s)
		}
		return err
	}); err != nil {
		return err
	}
	if err := r.PostJSON("/api/workflow/event/"+st.InstanceID, map[string]any{"approved": true, "comment": "compat"}).Want(200); err != nil {
		return err
	}
	return Eventually(60*time.Second, func() error {
		s, err := status()
		if err == nil && s != "complete" {
			err = fmt.Errorf("status %q, want complete", s)
		}
		return err
	})
}
