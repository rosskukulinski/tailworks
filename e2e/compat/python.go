package compat

// Cloudflare's Python Workers examples (cloudflare/python-workers-examples,
// issue #292), published in COMPAT.md's "python-workers" table. The ones
// that need no package vendored by pywrangler run unmodified; the rest are
// in pythonNotRun with the reason.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// pythonExamplesCommit is cloudflare/python-workers-examples main.
const pythonExamplesCommit = "47ec7d69f4052cd57bed5d3676cb7af76ca0656b" // 2026-11-24

// SectionPython is the COMPAT.md table of the Python examples.
const SectionPython = "python-workers"

func pyEx(id, dir string, f func(*Project)) *Project {
	p := Project{ID: id, Repo: "cloudflare/python-workers-examples", Commit: pythonExamplesCommit, Dir: dir,
		Section: SectionPython, Install: InstallNone, Exercises: []string{"python"}}
	f(&p)
	return &p
}

// uvSync is the README's `uv run pywrangler sync`: vendor the example's
// packages (and, from compatibility_date 2026-04-21, the workers SDK) into
// python_modules/ before tw deploy. Needs uv on PATH.
var uvSync = [][]string{{"uv", "run", "pywrangler", "sync"}}

const uvWorkaround = "`uv run pywrangler sync` vendors the packages into python_modules/ first (the README's step; tw deploy doesn't run pywrangler)"

// pyPkg is an example that needs its packages vendored.
func pyPkg(id, dir string, f func(*Project)) *Project {
	return pyEx(id, dir, func(p *Project) {
		p.Build = uvSync
		p.Workarounds = []string{uvWorkaround}
		f(p)
	})
}

// mcpHeaders are the MCP request headers of the mcp-server README.
func mcpHeaders(method, name string) []string {
	h := []string{"content-type", "application/json", "accept", "application/json, text/event-stream",
		"mcp-protocol-version", "2026-07-28", "mcp-method", method}
	if name != "" {
		h = append(h, "mcp-name", name)
	}
	return h
}

// todoProbe is the Todo-Backend round trip of the *-todo examples.
func todoProbe(r *Run) error {
	if err := r.PostJSON("/todos", map[string]any{"title": "compat todo"}).Want(201, "compat todo"); err != nil {
		if err2 := r.PostJSON("/todos", map[string]any{"title": "compat todo"}).Want(200, "compat todo"); err2 != nil {
			return err
		}
	}
	return r.Get("/todos").Want(200, "compat todo")
}

// pyRefused is an example tw refuses, naming why.
func pyRefused(id, dir, what, err, why string) *Project {
	return pyEx(id, dir, func(p *Project) {
		p.What = what
		p.Expect = Expect{Kind: ExpectRefused, Compat: "python", Error: err, Why: why}
	})
}

func pythonProjects() []*Project {
	return []*Project{
		pyEx("py-hello", "hello", func(p *Project) {
			p.What = "Python Worker: fetch handler"
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "Hello world!") }
		}),
		pyEx("py-binding", "binding", func(p *Project) {
			p.What = "Python Worker: KV put and get"
			p.Exercises = []string{"python", "kv"}
			p.Files = map[string]string{"wrangler.jsonc": `{
	"name": "hello-python-bindings",
	"main": "src/entry.py",
	"compatibility_date": "2025-11-02",
	"compatibility_flags": ["python_workers"],
	"kv_namespaces": [{"binding": "FOO", "id": "foo"}],
	"observability": {"enabled": true}
}
`}
			p.Workarounds = []string{"the config's `<YOUR_KV_NAMESPACE_ID>` placeholder is set to a namespace name, as a user sets it to the ID of the namespace they created"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "baz") }
		}),
		pyEx("py-durable-objects", "durable-objects", func(p *Project) {
			p.What = "Python Durable Object: RPC methods, storage"
			p.Exercises = []string{"python", "do-rpc", "do-storage"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/l1/add/hello").Want(200, "Message sent"); err != nil {
					return err
				}
				if err := r.Get("/l1/add/again").Want(200, "Message sent"); err != nil {
					return err
				}
				if err := r.Get("/l1/show").Want(200, "hello\nagain"); err != nil {
					return err
				}
				return r.Get("/l2/show").Want(200, "No messages")
			}
		}),
		pyEx("py-chatroom", "chatroom", func(p *Project) {
			p.What = "Python Durable Object: hibernatable WebSocket chatroom, an HTML file read from disk"
			p.Exercises = []string{"python", "websockets", "hibernation"}
			p.Probe = pyChatroomProbe
		}),
		pyEx("py-opengraph", "opengraph", func(p *Project) {
			p.What = "Python Worker: HTMLRewriter over an internet fetch"
			p.Exercises = []string{"python", "fetch"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, `og:title`, "Example Domain") }
		}),
		pyEx("py-cron", "cron", func(p *Project) {
			p.What = "Python Worker: cron trigger every minute"
			p.Exercises = []string{"python", "cron"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/").Want(200, "Hello from Cron Worker"); err != nil {
					return err
				}
				_, err := r.WaitLog("Scheduled task has been executed.", 150*time.Second)
				return err
			}
		}),
		pyRefused("py-workflows", "workflows", "Python Workflow", "workflows: not supported for Python Workers",
			"Python Workflows aren't supported yet (docs/python.md#limits)"),
		pyEx("py-assets", "assets", func(p *Project) {
			p.What = "Python Worker serving static assets through env.ASSETS"
			p.Exercises = []string{"python", "assets"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/image.svg").Want(200, "<svg"); err != nil {
					return err
				}
				return r.Get("/favicon.ico").Want(200)
			}
		}),
		pyEx("py-js-api-pygments", "js-api-pygments", func(p *Project) {
			// The example is two Workers: ts/ calls py/'s highlight_code()
			// over the PYTHON_RPC service binding (service "py-rpc-server").
			p.Dir = "js-api-pygments/ts"
			p.What = "a TypeScript Worker calling a Python Worker's RPC method (Pygments) over a service binding"
			p.Exercises = []string{"python", "service-bindings"}
			p.Companions = []*Project{pyPkg("py-rpc-server", "js-api-pygments/py", func(c *Project) {
				c.Section = ""
				c.What = "the Python RPC server of js-api-pygments"
				c.Probe = func(r *Run) error { return r.Get("/").Want(200, "Python RPC server is running") }
			})}
			p.Workarounds = []string{uvWorkaround + ", in py/", "py/ is deployed first, as the workload py-rpc-server its wrangler config names"}
			p.ExpectDev = Expect{Kind: ExpectProbeFail, Compat: "service-bindings",
				Why: "`tw dev` runs one Worker and has no binding to another workload, so env.PYTHON_RPC reaches nothing"}
			p.Probe = func(r *Run) error {
				return r.Get("/").Want(200, "Language Detected:</strong> TypeScript", `<div class="highlight">`, "fetchUserData", ".highlight .k")
			}
		}),
		pyPkg("py-url-shortener", "url-shortener", func(p *Project) {
			p.What = "Python Worker on KV, compatibility_date 2026-09-01 (the vendored workers SDK)"
			p.Exercises = []string{"python", "kv"}
			p.Files = map[string]string{"wrangler.jsonc": `{
	"name": "python-url-shortener",
	"main": "src/entry.py",
	"compatibility_date": "2026-09-01",
	"compatibility_flags": ["python_workers"],
	"kv_namespaces": [{"binding": "LINKS", "id": "links"}],
	"observability": {"enabled": true}
}
`}
			p.Workarounds = append(p.Workarounds, "the config's `<YOUR_KV_NAMESPACE_ID>` placeholder is set to a namespace name, as a user sets it to the ID of the namespace they created")
			p.Probe = func(r *Run) error {
				res := r.PostJSON("/shorten", map[string]string{"url": "https://example.com/path"})
				var out struct {
					Code string `json:"code"`
				}
				if err := res.Want(200, "short_url"); err != nil {
					if err2 := res.Want(201, "short_url"); err2 != nil {
						return err
					}
				}
				if err := res.JSON(&out); err != nil || out.Code == "" {
					return fmt.Errorf("POST /shorten: no code in %s", res)
				}
				red := r.Get("/" + out.Code)
				if red.Code != 302 || red.Header.Get("location") != "https://example.com/path" {
					return fmt.Errorf("GET /%s: %d location %q, want 302 to https://example.com/path", out.Code, red.Code, red.Header.Get("location"))
				}
				return nil
			}
		}),
		pyPkg("py-fastapi", "fastapi", func(p *Project) {
			p.What = "FastAPI + Jinja2 (pydantic-core, a compiled Pyodide wheel)"
			p.Exercises = []string{"python", "vars"}
			p.Probe = func(r *Run) error {
				if err := r.Get("/hi/tailnet").Want(200, "Hello, tailnet!"); err != nil {
					return err
				}
				return r.Get("/env").Want(200, "Here is an example of getting an environment variable")
			}
		}),
		pyPkg("py-django", "django", func(p *Project) {
			p.What = "Django"
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "Hello from Django") }
		}),
		pyPkg("py-django-markdown-r2", "django-markdown-r2", func(p *Project) {
			p.What = "Django + Markdown, a Durable Object and an R2 bucket"
			p.Exercises = []string{"python", "r2", "do-storage"}
			p.Probe = func(r *Run) error { return r.Get("/").Want(200, "<title>Artic") }
		}),
		pyPkg("py-django-todo-d1", "django-todo-d1", func(p *Project) {
			p.What = "Django Todo-Backend on D1 (django-cf), D1 migrations"
			p.Exercises = []string{"python", "d1", "d1-migrations"}
			p.Setup = [][]string{{"d1", "migrations", "apply", "django-todo-d1"}}
			p.Probe = todoProbe
		}),
		pyPkg("py-mcp-server", "mcp-server", func(p *Project) {
			p.What = "MCP server (the Python MCP SDK) on D1, Python 3.14"
			p.Exercises = []string{"python", "mcp", "d1", "d1-migrations"}
			p.Setup = [][]string{{"d1", "migrations", "apply", "mcp-incidents"}}
			p.ExpectDeployed = Expect{Kind: ExpectProbeFail, Compat: "python",
				Why: "the example's MCP app keeps the Python MCP SDK's default host 127.0.0.1, which turns on DNS-rebinding protection allowing only localhost Host headers, so a call by the deployed name gets 421 \"Invalid Host header\" (a workers.dev name would too); under `tw dev` the Host is 127.0.0.1"}
			p.Probe = func(r *Run) error {
				body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"open_incident","arguments":{"title":"API latency","severity":"high"}}}`
				return r.Do("POST", "/mcp", []byte(body), mcpHeaders("tools/call", "open_incident")...).Want(200, `API latency`, `\"status\": \"open\"`)
			}
		}),
		pyPkg("py-image-gen", "image-gen", func(p *Project) {
			p.What = "Pillow image generation (a compiled Pyodide wheel)"
			p.Probe = func(r *Run) error {
				res := r.Get("/gradient?width=60&height=30&color1=FF6B6B&color2=4ECDC4")
				if err := res.Want(200, "PNG"); err != nil {
					return err
				}
				if ct := res.Header.Get("content-type"); ct != "image/png" {
					return fmt.Errorf("GET /gradient: content-type %q, want image/png", ct)
				}
				return nil
			}
		}),
		pyPkg("py-sync-http-clients", "sync-http-clients", func(p *Project) {
			p.What = "requests, urllib3 and httpx.Client fetching the internet"
			p.Exercises = []string{"python", "fetch"}
			p.Probe = func(r *Run) error { return r.Get("/sync").Want(200, `"saw_expected_text": true`) }
		}),
	}
}

// pyChatroomProbe: the page, then two clients in a room: one's message
// reaches the other.
func pyChatroomProbe(r *Run) error {
	if err := r.Get("/").Want(200, "<title>Chatroom</title>"); err != nil {
		return err
	}
	a, err := r.WS("/room/compat")
	if err != nil {
		return err
	}
	defer a.CloseNow()
	if m, err := WSRead(a, 10*time.Second); err != nil || !strings.Contains(m, "Connected to chatroom") {
		return fmt.Errorf("first client's welcome: %q %v", m, err)
	}
	b, err := r.WS("/room/compat")
	if err != nil {
		return err
	}
	defer b.CloseNow()
	WSRead(b, 10*time.Second) // its welcome
	msg, _ := json.Marshal(map[string]string{"username": "compat", "text": "hello from the tailnet"})
	if err := WSWrite(b, string(msg)); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m, err := WSRead(a, time.Until(deadline))
		if err != nil {
			return fmt.Errorf("the first client got no broadcast: %v", err)
		}
		if strings.Contains(m, "hello from the tailnet") {
			r.Note("broadcast %q", tail(m, 200))
			return nil
		}
	}
	return fmt.Errorf("the first client got no broadcast")
}

// pythonNotRun are the Python examples without a Project.
func pythonNotRun() []NotRun {
	py := func(why string, dirs ...string) NotRun {
		return NotRun{Section: SectionPython, Repo: "cloudflare/python-workers-examples", Commit: pythonExamplesCommit, Dirs: dirs, Why: why}
	}
	return []NotRun{
		py("needs Workers AI (an `ai` binding), which tailworks doesn't provide",
			"image-redraw", "langchain", "vectorize-rag", "workers-ai"),
		py("needs Hyperdrive", "hyperdrive-batched-counter"),
		py("needs the Worker Loader (`worker_loaders` binding), which tailworks keeps to the platform", "dynamic-py-py"),
		py("needs its database seeded after the deploy (`d1 execute --file db_init.sql`), which `tw dev` runs can't do; run by `make e2e-compat` as `py-query-d1`, `py-fastapi-todo` and `py-flask-todo`",
			"fastapi-todo", "flask-todo", "query-d1"),
		py("a long-running Durable Object on the public Bluesky firehose (an outbound WebSocket); not tried", "websocket-stream-consumer"),
		py("the repo's test harness, not an example", "tests"),
	}
}

// pythonDeployedOnly are Python examples only the deployed run can
// prepare (post-deploy steps), outside the published table.
func pythonDeployedOnly() []*Project {
	return []*Project{
		{ID: "py-query-d1", Repo: "cloudflare/python-workers-examples", Commit: pythonExamplesCommit, Dir: "query-d1",
			What: "Python Worker on D1", Exercises: []string{"python", "d1"}, Install: InstallNone,
			Setup: [][]string{{"d1", "execute", "quotes", "--file", "db_init.sql"}},
			Probe: func(r *Run) error { return r.Get("/").Want(200, `"quote"`, `"author"`) }},
		{ID: "py-fastapi-todo", Repo: "cloudflare/python-workers-examples", Commit: pythonExamplesCommit, Dir: "fastapi-todo",
			What: "FastAPI Todo-Backend on D1", Exercises: []string{"python", "d1"}, Install: InstallNone,
			Build: uvSync, Workarounds: []string{uvWorkaround},
			Setup: [][]string{{"d1", "execute", "todos", "--file", "db_init.sql"}}, Probe: todoProbe},
		{ID: "py-flask-todo", Repo: "cloudflare/python-workers-examples", Commit: pythonExamplesCommit, Dir: "flask-todo",
			What: "Flask Todo-Backend on D1", Exercises: []string{"python", "d1"}, Install: InstallNone,
			Build: uvSync, Workarounds: []string{uvWorkaround},
			Setup: [][]string{{"d1", "execute", "todos", "--file", "db_init.sql"}}, Probe: todoProbe},
	}
}
