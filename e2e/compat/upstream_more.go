package compat

// The second pass over the upstream examples of issue #325: the
// cloudflare/agents `examples/next/*` examples that declare no
// Cloudflare-only binding, and the Vercel AI SDK's Fastify, Nest and MCP
// examples. upstream.go has the first pass and the not-run list.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// aiMCPVersion is vercel/ai packages/mcp at vercelAICommit.
const aiMCPVersion = "2.0.104"

// upstreamMoreProjects are appended to Projects() after upstreamProjects.
func upstreamMoreProjects() []*Project {
	return []*Project{
		// --- cloudflare/agents examples/next (#325) ---
		agentsUp("ag-next-lifecycle", "next/lifecycle", func(p *Project) {
			p.What = "A plain `DurableObject` with `Lifecycle` and a job-queue capability: a self-rescheduling durable job, cancel, `ctx.abort()` restart"
			p.Exercises = []string{"agents-sdk", "do-storage", "alarms"}
			p.Probe = probeNextLifecycle
		}),
		agentsUp("ag-next-mcp-client", "next/mcp-client", func(p *Project) {
			p.What = "`MCPClientManager` as a lifecycle capability on a plain `DurableObject`: register, connect, catalog, remove (+ React UI)"
			p.Exercises = []string{"agents-sdk", "mcp", "do-storage", "assets"}
			p.Build = viteBuild
			p.Probe = probeNextMCPClient
		}),
		agentsUp("ag-next-queue", "next/queue", func(p *Project) {
			p.What = "`Queue` as a lifecycle capability: durable background items run from the alarm loop"
			p.Exercises = []string{"agents-sdk", "do-storage", "alarms"}
			p.Probe = probeNextQueue
		}),
		agentsUp("ag-next-routing", "next/routing", func(p *Project) {
			p.What = "`RoutedAgents` on a plain `DurableObject` hub: one Agent per chat, forwarded requests and WebSockets, pushed metadata, search, delete (+ React UI)"
			p.Exercises = []string{"agents-sdk", "do-rpc", "do-storage", "websockets", "assets"}
			p.Build = viteBuild
			p.Probe = probeNextRouting
		}),
		agentsUp("ag-next-schedules", "next/schedules", func(p *Project) {
			p.What = "`Scheduler` as a lifecycle capability: a one-shot reminder delivered from the alarm, a cron schedule, cancel"
			p.Exercises = []string{"agents-sdk", "do-storage", "alarms"}
			p.Probe = probeNextSchedules
		}),
		agentsUp("ag-next-sessions", "next/sessions", func(p *Project) {
			p.What = "`Sessions` on a plain `DurableObject`: a message tree, streamed (NDJSON) history, branches, a compaction overlay"
			p.Exercises = []string{"agents-sdk", "do-storage", "fetch"}
			p.Probe = probeNextSessions
		}),
		agentsUp("ag-next-streams", "next/streams", func(p *Project) {
			p.What = "`Streams` with `Tasks`: a durable task appends chunks to a durable stream, read over SSE, resumed from a cursor"
			p.Exercises = []string{"agents-sdk", "do-storage", "alarms", "fetch"}
			p.Probe = probeNextStreams
		}),
		agentsUp("ag-next-tasks", "next/tasks", func(p *Project) {
			p.What = "`Tasks`: a durable replayable run with steps and a durable sleep, idempotency keys, run lookup"
			p.Exercises = []string{"agents-sdk", "do-storage", "alarms"}
			p.Probe = probeNextTasks
		}),
		agentsUp("ag-next-websockets", "next/websockets", func(p *Project) {
			p.What = "The `WebSockets` capability on a plain `DurableObject`: the Agent protocol (`useAgent`), room frames, `rpc` callables incl. a streamed one, tags, HTTP broadcast (+ React UI)"
			p.Exercises = []string{"agents-sdk", "websockets", "hibernation", "do-storage", "assets"}
			p.Build = viteBuild
			p.Probe = probeNextWebSockets
		}),

		// --- Vercel AI SDK examples (#325) ---
		nodeServerEx("ai-fastify", "examples/fastify", "./src/server", 8080, true, func(p *Project) {
			p.What = "Fastify 5 server: `streamText` as a UI message stream, and `createUIMessageStream` with custom data"
			p.Expect = Expect{Kind: ExpectProbeFail, Compat: "nodejs-compat",
				Why: "not a tailworks gap: Fastify doesn't run as a Worker. It starts, but `fastify.listen()` never reaches the node:http bridge (`Http server with port 8080 not found`), and stock `wrangler dev` doesn't start it at all (its bundle of Fastify's pino logger throws `Cannot destructure property 'serializersSym' of 'pino.symbols'`)"}
			p.Probe = func(r *Run) error {
				if err := r.Do("POST", "/", nil).Want(200, `"type":"text-delta"`, stubText); err != nil {
					return err
				}
				return r.Do("POST", "/stream-data", nil).Want(200, `"type":"data-custom"`, "Hello, world!", stubText)
			}
		}),
		nodeServerEx("ai-nest", "examples/nest", "./dist/main.js", 8080, true, func(p *Project) {
			p.What = "Nest server (Express platform): `streamText` piped with `pipeUIMessageStreamToResponse`, and custom data"
			p.Build = npmBuild
			p.Expect = Expect{Kind: ExpectRefused, Compat: "wrangler-bundling", Error: `Could not resolve "@nestjs/microservices"`,
				Why: "not a tailworks gap: Nest `require()`s its optional packages (`@nestjs/microservices`, `@nestjs/websockets`, `class-validator`, `class-transformer`) in code a bundler can't leave out, so the Worker doesn't bundle: `Could not resolve \"@nestjs/microservices\"`. Stock `wrangler dev` fails the same way"}
			// `nest build` compiles every .ts file in the directory: the
			// added entry and stub are .js, which it leaves alone.
			for _, f := range []string{"tw-compat-entry", "tw-compat-openai"} {
				p.Files[f+".js"] = p.Files[f+".ts"]
				delete(p.Files, f+".ts")
			}
			p.Files["wrangler.jsonc"] = strings.Replace(p.Files["wrangler.jsonc"], "tw-compat-entry.ts", "tw-compat-entry.js", 1)
			// @nestjs/core ^11 with @nestjs/common ^10: npm refuses the peer
			// conflict that the monorepo's pnpm install accepts.
			p.NpmArgs = []string{"--legacy-peer-deps"}
			// `import 'dotenv/config'` in src/main.ts, without a dotenv
			// dependency: the monorepo root provides it.
			p.AddDeps = map[string]string{"dotenv": "16.4.5", "zod": zodVersion}
			p.Workarounds = append(p.Workarounds, "npm install --legacy-peer-deps: the example pins `@nestjs/core` ^11 next to `@nestjs/common` ^10, a peer conflict npm refuses (pnpm, in the monorepo, accepts it); so `zod`@"+zodVersion+", a peer of `ai` that npm then leaves out, is added", "dotenv@16.4.5 added: `src/main.ts` imports `dotenv/config`, which the example's package.json leaves to the monorepo")
			p.Changes[0] = "the example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: 8080 })` from `cloudflare:node`) are added; the entry runs `dist/main.js`, the README's `nest build` output; `src/` is unchanged"
			p.Probe = func(r *Run) error {
				if err := r.Do("POST", "/", nil).Want(200, `"type":"text-delta"`, stubText); err != nil {
					return err
				}
				return r.Do("POST", "/stream-data", nil).Want(200, `"type":"data-custom"`, "Hello, world!", stubText)
			}
		}),
		nodeServerEx("ai-mcp-http", "examples/mcp", "./src/http/server", 3000, false, func(p *Project) {
			p.What = "`mcp`'s `http` server: an Express MCP server on the Streamable HTTP transport (MCP SDK v1, stateless), the one its `client:http` calls"
			p.Exercises = []string{"vercel-ai", "mcp", "nodejs-compat"}
			p.Pins["@ai-sdk/mcp"] = aiMCPVersion
			p.Workarounds = []string{aiPin + `; "@ai-sdk/mcp" likewise ` + aiMCPVersion}
			p.Probe = func(r *Run) error {
				m, name, err := r.MCPConnect("/mcp")
				if err != nil {
					return err
				}
				if name != "example-http-server" {
					return fmt.Errorf("serverInfo.name %q", name)
				}
				out, err := m.Tool("get-user-info", map[string]any{"userId": "foo_123"})
				if err != nil || !strings.Contains(out, "Here is information about user foo_123:") || !strings.Contains(out, "john.doe@example.com") {
					return fmt.Errorf("get-user-info: %q %v", out, err)
				}
				return r.Get("/mcp").Want(405, "Method not allowed")
			}
		}),
		nodeServerEx("ai-mcp-sse", "examples/mcp", "./src/sse/server", 8080, false, func(p *Project) {
			p.What = "`mcp`'s `sse` server: an Express MCP server on the legacy SSE transport (`GET /sse`, `POST /messages`), the one its `client:sse` calls"
			p.Exercises = []string{"vercel-ai", "mcp", "nodejs-compat"}
			p.Pins["@ai-sdk/mcp"] = aiMCPVersion
			p.Workarounds = []string{aiPin + `; "@ai-sdk/mcp" likewise ` + aiMCPVersion}
			p.Expect = Expect{Kind: ExpectProbeFail, Compat: "nodejs-compat",
				Why: "not a tailworks gap: the server keeps the `GET /sse` response in a module variable and writes each answer to it while handling a later `POST /messages`, and a Worker can't write to one request's response from another (stock `wrangler dev` cancels the request the same way: the stream gets the endpoint event, the `POST` gets 202, no answer arrives). Its `http` server, the Streamable HTTP transport, passes"}
			p.Probe = probeMCPSSE
		}),
	}
}

// nodeServerEx is an AI SDK example that is a Node server listening on
// port (node:http, Express, Fastify, Nest). module is the server module
// the added entry imports; it runs as a Worker through Cloudflare's own
// node:http bridge, `httpServerHandler` from `cloudflare:node`. stub: the
// example calls OpenAI, and the entry answers those calls with a scripted
// stream (openAIStub).
func nodeServerEx(id, dir, module string, port int, stub bool, f func(*Project)) *Project {
	p := Project{ID: id, Repo: "vercel/ai", Commit: vercelAICommit, Dir: dir, Section: SectionVercel}
	p.Exercises = []string{"vercel-ai", "nodejs-compat"}
	p.Pins = map[string]string{"ai": AIVersion, "@ai-sdk/openai": AIOpenAIVersion}
	p.Install = InstallUpgrade
	head := "// Written by e2e/compat: the Worker entry. "
	imp := ""
	if stub {
		head += "The first import answers the\n// example's OpenAI calls with a scripted stream; the second runs the\n// example's server module, unchanged"
		imp = "import \"./tw-compat-openai\";\n"
	} else {
		head += "The import runs the example's\n// server module, unchanged"
	}
	entry := fmt.Sprintf("%s (it listens on port %d); the export\n// is Cloudflare's node:http bridge to that port.\n%simport %q;\nimport { httpServerHandler } from \"cloudflare:node\";\n\nexport default httpServerHandler({ port: %d });\n",
		head, port, imp, module, port)
	p.Files = map[string]string{
		"wrangler.jsonc":     "// Written by e2e/compat.\n{\n  \"name\": \"" + id + "\",\n  \"main\": \"tw-compat-entry.ts\",\n  \"compatibility_date\": \"2026-06-11\",\n  \"compatibility_flags\": [\"nodejs_compat\"]\n}\n",
		"tw-compat-entry.ts": entry,
	}
	p.Workarounds = []string{aiPin}
	p.Changes = []string{fmt.Sprintf("the example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: %d })` from `cloudflare:node`) are added; `src/` is unchanged", port)}
	if stub {
		p.Files["tw-compat-openai.ts"] = openAIStub
		p.Secrets = map[string]string{"OPENAI_API_KEY": "tw-compat-no-key"}
		p.Changes = append(p.Changes, noKeyChange)
	}
	f(&p)
	return &p
}

// compatName is an object name unique to this run (a deployed host can
// keep an object's storage from an earlier run).
func compatName() string { return fmt.Sprintf("compat-%d", time.Now().UnixNano()%1e9) }

// getJSON GETs path and decodes the JSON body into v.
func getJSON(r *Run, path string, v any) error {
	res := r.Get(path)
	if err := res.Want(200); err != nil {
		return err
	}
	return res.JSON(v)
}

// --- cloudflare/agents examples/next probes ---

func probeNextLifecycle(r *Run) error {
	base := "/agents/do-agent/" + compatName()
	var st struct {
		Isolate  struct{ ID string }
		Counters map[string]int
		Jobs     []struct{ ID, Fn string }
		Log      []struct{ Event, Detail string }
	}
	if err := getJSON(r, base+"/status", &st); err != nil {
		return err
	}
	if st.Isolate.ID == "" {
		return fmt.Errorf("/status: no isolate id")
	}
	if err := r.Do("POST", base+"/start", nil).Want(200, `"ok":true`); err != nil {
		return err
	}
	// The tick job runs now and reschedules itself every 5 s (the alarm).
	if err := Eventually(40*time.Second, func() error {
		if err := getJSON(r, base+"/status", &st); err != nil {
			return err
		}
		if st.Counters["ticks"] < 2 {
			return fmt.Errorf("ticks = %d, want 2 (jobs %+v)", st.Counters["ticks"], st.Jobs)
		}
		return nil
	}); err != nil {
		return err
	}
	r.Note("the tick job ran %d times", st.Counters["ticks"])
	// ctx.abort(): the instance restarts, the durable job and counters stay.
	if err := r.Do("POST", base+"/restart", nil).Want(200, `"ok":true`); err != nil {
		return err
	}
	before := st.Counters["ticks"]
	if err := Eventually(40*time.Second, func() error {
		if err := getJSON(r, base+"/status", &st); err != nil {
			return err
		}
		if st.Counters["ticks"] <= before {
			return fmt.Errorf("after the restart: ticks = %d, want more than %d", st.Counters["ticks"], before)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.Do("POST", base+"/stop", nil).Want(200, `"ok":true`); err != nil {
		return err
	}
	if err := getJSON(r, base+"/status", &st); err != nil {
		return err
	}
	for _, j := range st.Jobs {
		if j.ID == "tick" {
			return fmt.Errorf("after /stop the tick job is still queued: %+v", st.Jobs)
		}
	}
	r.Note("not exercised: /oom and /oom-real (the memory-limit circuit breaker backs off 30 s and 60 s; a real OOM kills the isolate)")
	return nil
}

func probeNextSchedules(r *Run) error {
	base := "/agents/reminder-object/" + compatName()
	var created struct {
		Created struct{ ID string }
	}
	res := r.PostJSON(base+"/reminders", map[string]any{"message": "compat reminder", "seconds": 2})
	if err := res.Want(201); err != nil {
		return err
	}
	if err := res.JSON(&created); err != nil || created.Created.ID == "" {
		return fmt.Errorf("create: %v %s", err, res)
	}
	var cron struct {
		Created struct{ ID string }
	}
	res = r.PostJSON(base+"/reminders", map[string]any{"message": "compat tick", "cron": "* * * * *"})
	if err := res.Want(201); err != nil {
		return err
	}
	if err := res.JSON(&cron); err != nil || cron.Created.ID == "" {
		return fmt.Errorf("create cron: %v %s", err, res)
	}
	// The alarm delivers the one-shot reminder; the callback records it.
	if err := Eventually(40*time.Second, func() error {
		return r.Get(base).Want(200, `"message":"compat reminder"`, `"deliveredBy":"compat-`)
	}); err != nil {
		return err
	}
	if err := r.Do("DELETE", base+"/reminders/"+cron.Created.ID, nil).Want(200, `"cancelled":true`); err != nil {
		return err
	}
	res = r.Get(base)
	if err := res.Want(200); err != nil {
		return err
	}
	if strings.Contains(res.Body, cron.Created.ID) {
		return fmt.Errorf("the cancelled cron schedule is still pending: %s", res)
	}
	return nil
}

func probeNextQueue(r *Run) error {
	base := "/agents/upload-object/" + compatName()
	res := r.PostJSON(base+"/uploads", map[string]any{"fileName": "compat.pdf", "bytes": 1024})
	if err := res.Want(202, `"queued"`); err != nil {
		return err
	}
	return Eventually(30*time.Second, func() error {
		return r.Get(base).Want(200, `"fileName":"compat.pdf"`, `"processedBy":"compat-`, `"pending":[]`)
	})
}

func probeNextTasks(r *Run) error {
	base := "/agents/report-object/" + compatName()
	var rc struct {
		Receipt struct {
			RunID    string
			Accepted bool
			Status   string
		}
	}
	res := r.PostJSON(base+"/reports", map[string]any{"topic": "compat", "holdSeconds": 3})
	if err := res.Want(201); err != nil {
		return err
	}
	if err := res.JSON(&rc); err != nil || !rc.Receipt.Accepted || rc.Receipt.RunID == "" {
		return fmt.Errorf("start: %v %s", err, res)
	}
	runID := rc.Receipt.RunID
	// The same idempotency key joins the run.
	res = r.PostJSON(base+"/reports", map[string]any{"topic": "compat", "holdSeconds": 3})
	if err := res.Want(200); err != nil {
		return err
	}
	if err := res.JSON(&rc); err != nil || rc.Receipt.Accepted || rc.Receipt.RunID != runID {
		return fmt.Errorf("repeat: want the same run, not accepted: %v %s", err, res)
	}
	if err := r.Get(base+"/reports/"+runID).Want(200, runID); err != nil {
		return err
	}
	// The run parks on the 3 s durable sleep; the alarm wakes it to publish.
	if err := Eventually(40*time.Second, func() error {
		return r.Get(base).Want(200, `"published":[{`, `"topic":"compat"`, `"publishedBy":"compat-`)
	}); err != nil {
		return err
	}
	return r.Get(base+"/reports/"+runID).Want(200, `"completed"`)
}

func probeNextStreams(r *Run) error {
	base := "/agents/generate-object/" + compatName()
	if err := r.PostJSON(base+"/generate", map[string]any{"id": "compat", "total": 4}).Want(201, `"accepted":true`); err != nil {
		return err
	}
	// The SSE read replays, tails the live appends (500 ms apart) and ends
	// with `done` when the producer closes the stream.
	res := r.Get(base + "/streams/compat")
	if err := res.Want(200, "id: 0\n", "chunk 3 of 4", "event: done"); err != nil {
		return err
	}
	if ct := res.Header.Get("content-type"); !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("content-type %q", ct)
	}
	// From a cursor: the replay starts there.
	res = r.Get(base + "/streams/compat?from=2")
	if err := res.Want(200, "id: 2\n", "chunk 3 of 4", "event: done"); err != nil {
		return err
	}
	if strings.Contains(res.Body, "chunk 0 of 4") {
		return fmt.Errorf("?from=2 replayed chunk 0: %s", res)
	}
	return r.Get(base).Want(200, `"runs":[{`, `"completed"`)
}

func probeNextSessions(r *Run) error {
	base := "/agents/session-object/" + compatName()
	msg := func(id, role, text string) map[string]any {
		return map[string]any{"id": id, "role": role, "parts": []any{map[string]any{"type": "text", "text": text}}}
	}
	if err := r.PostJSON(base+"/messages", msg("m1", "user", "hello")).Want(201); err != nil {
		return err
	}
	if err := r.PostJSON(base+"/messages?parent=m1", msg("m2", "assistant", "hi")).Want(201); err != nil {
		return err
	}
	// A second child of m1: a branch.
	if err := r.PostJSON(base+"/messages?parent=m1", msg("m3", "assistant", "hey there")).Want(201); err != nil {
		return err
	}
	res := r.Get(base + "/history?leaf=m2")
	if err := res.Want(200, `"id":"m1"`, `"id":"m2"`); err != nil {
		return err
	}
	if ct := res.Header.Get("content-type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		return fmt.Errorf("history content-type %q", ct)
	}
	if strings.Contains(res.Body, `"id":"m3"`) {
		return fmt.Errorf("the path to m2 includes m3: %s", res)
	}
	if err := r.Get(base+"/branches/m1").Want(200, `"m2"`, `"m3"`); err != nil {
		return err
	}
	if err := r.PostJSON(base+"/compactions", map[string]any{"summary": "The user greeted the assistant.", "fromMessageId": "m1", "toMessageId": "m2"}).Want(201); err != nil {
		return err
	}
	if err := r.Get(base+"/messages/m3").Want(200, "hey there"); err != nil {
		return err
	}
	// The active path (m1 → m3, the last append) and its leaf.
	return r.Get(base).Want(200, `"messages":2`, `"leaf":"m3"`)
}

func probeNextWebSockets(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	room := compatName()
	base := "/agents/room-object/" + room
	a, err := r.Agent(base + "?nick=alice")
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err := a.Wait(15*time.Second, func(m string) bool { return strings.Contains(m, `"type":"history"`) }); err != nil {
		return err
	}
	// A room frame.
	if err := a.Send(`{"type":"say","text":"hello from the socket"}`); err != nil {
		return err
	}
	if _, err := a.Wait(15*time.Second, func(m string) bool {
		return strings.Contains(m, `"type":"message"`) && strings.Contains(m, "hello from the socket")
	}); err != nil {
		return err
	}
	// The state the capability keeps on the socket (survives hibernation).
	if err := a.Send(`{"type":"whoami"}`); err != nil {
		return err
	}
	if _, err := a.Wait(15*time.Second, func(m string) bool {
		return strings.Contains(m, `"type":"whoami"`) && strings.Contains(m, `"nick":"alice"`)
	}); err != nil {
		return err
	}
	// rpc frames against the room's RpcTarget, one streamed.
	if out, err := a.RPC("say", "alice", "from rpc"); err != nil || !strings.Contains(string(out), "from rpc") {
		return fmt.Errorf("say: %s %v", out, err)
	}
	_, chunks, err := a.RPCStream("countdown", 3)
	if err != nil {
		return err
	}
	if len(chunks) != 4 || chunks[0] != "3" || chunks[3] != "0" {
		return fmt.Errorf("countdown(3) chunks: %v", chunks)
	}
	// HTTP: the history, members by tag, and a broadcast from a request.
	if err := r.Get(base+"/history").Want(200, "hello from the socket", "from rpc"); err != nil {
		return err
	}
	if err := r.Get(base+"/members?nick=alice").Want(200, `["`); err != nil {
		return err
	}
	a.Forget()
	if err := r.PostJSON(base+"/say", map[string]any{"text": "deploy finished", "nick": "ci"}).Want(200, "deploy finished"); err != nil {
		return err
	}
	_, err = a.Wait(15*time.Second, func(m string) bool {
		return strings.Contains(m, `"type":"message"`) && strings.Contains(m, "deploy finished")
	})
	if err == nil {
		r.Note("not exercised: the Cap'n Web transport (`transport: \"capnweb\"`)")
	}
	return err
}

func probeNextRouting(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	user := compatName()
	hubPath := "/agents/user-hub/" + user
	hub, err := r.Agent(hubPath)
	if err != nil {
		return err
	}
	defer hub.Close()
	out, err := hub.RPC("createChat")
	if err != nil {
		return err
	}
	var chat string
	if json.Unmarshal(out, &chat); chat == "" {
		return fmt.Errorf("createChat: %s", out)
	}
	chatPath := hubPath + "/chats/" + chat
	// Forwarded by the hub to the chat's own Agent, which pushes its
	// metadata back to the hub (DO RPC both ways).
	if err := r.PostJSON(chatPath+"/messages", map[string]any{"role": "user", "text": "How do facets work?"}).Want(200, "How do facets work?"); err != nil {
		return err
	}
	if err := r.PostJSON(chatPath+"/messages", map[string]any{"role": "assistant", "text": "Any alternatives?"}).Want(200, "Any alternatives?"); err != nil {
		return err
	}
	if out, err := hub.RPC("listChats"); err != nil || !strings.Contains(string(out), `"title":"How do facets work?"`) || !strings.Contains(string(out), `"lastMessage":"Any alternatives?"`) {
		return fmt.Errorf("listChats: %s %v", out, err)
	}
	if out, err := hub.RPC("searchChats", "facets"); err != nil || !strings.Contains(string(out), chat) {
		return fmt.Errorf("searchChats: %s %v", out, err)
	}
	if err := r.Get(hubPath).Want(200, chat); err != nil {
		return err
	}
	// A WebSocket through the hub's route is the chat's own.
	c, err := r.Agent(chatPath)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Send("ping"); err != nil {
		return err
	}
	if _, err := c.Wait(15*time.Second, func(m string) bool { return m == "echo:ping" }); err != nil {
		return err
	}
	if out, err := hub.RPC("deleteChat", chat); err != nil || string(out) != "true" {
		return fmt.Errorf("deleteChat: %s %v", out, err)
	}
	return r.Get(chatPath + "/messages").Want(404)
}

func probeNextMCPClient(r *Run) error {
	if err := r.Get("/", "sec-fetch-mode", "navigate").Want(200, "<html"); err != nil {
		return err
	}
	base := "/agents/mcp-client-object/" + compatName()
	if err := r.Get(base).Want(200, `"servers":[]`, `"tools":[]`); err != nil {
		return err
	}
	if err := r.PostJSON(base+"/connect", map[string]any{"name": "x"}).Want(400); err != nil {
		return err
	}
	// A server nothing answers on: it is registered (persisted), and its
	// connection fails; the catalog shows it.
	res := r.PostJSON(base+"/connect", map[string]any{"name": "nowhere", "url": "http://mcp.invalid/mcp"})
	if res.Err != nil {
		return res.Err
	}
	r.Note("connect(unreachable) → %d %s", res.Code, tail(res.Body, 200))
	if err := r.Get(base).Want(200, `"name":"nowhere"`, `"url":"http://mcp.invalid/mcp"`); err != nil {
		return err
	}
	if err := r.Do("DELETE", base+"/servers/nowhere", nil).Want(204); err != nil {
		return err
	}
	if err := r.Get(base).Want(200, `"servers":[]`); err != nil {
		return err
	}
	r.Note("not exercised: a reachable MCP server and its OAuth popup (the run's tailnet policy lets the workload reach no peer; e2e/sdk AgentAsMCPClient covers it)")
	return nil
}

// --- the Vercel AI SDK's mcp/sse server: an MCP client on the SSE transport ---

// probeMCPSSE is what the example's client:sse does (createMCPClient with
// `type: 'sse'`): GET /sse, read the endpoint event, POST JSON-RPC there,
// read the answers on the event stream.
func probeMCPSSE(r *Run) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", r.Base+"/sse", nil)
	req.Header.Set("accept", "text/event-stream")
	resp, err := (&http.Client{Transport: r.HC.Transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("content-type"), "text/event-stream") {
		return fmt.Errorf("GET /sse: %d %q", resp.StatusCode, resp.Header.Get("content-type"))
	}
	r.Note("GET /sse → 200 event stream")
	type event struct{ name, data string }
	events := make(chan event, 16)
	var once sync.Once
	go func() {
		defer once.Do(func() { close(events) })
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev event
		for sc.Scan() {
			l := sc.Text()
			switch {
			case l == "":
				if ev.data != "" {
					events <- ev
				}
				ev = event{}
			case strings.HasPrefix(l, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(l, "event:"))
			case strings.HasPrefix(l, "data:"):
				ev.data += strings.TrimSpace(strings.TrimPrefix(l, "data:"))
			}
		}
	}()
	next := func(want string) (string, error) {
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return "", fmt.Errorf("the event stream ended before a %q event", want)
				}
				if ev.name == want || (want == "message" && ev.name == "") {
					return ev.data, nil
				}
			case <-ctx.Done():
				return "", fmt.Errorf("no %q event: %v", want, ctx.Err())
			}
		}
	}
	endpoint, err := next("endpoint")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(endpoint, "/messages?sessionId=") {
		return fmt.Errorf("endpoint event %q", endpoint)
	}
	r.Note("endpoint %s", endpoint)
	id := 0
	call := func(method string, params any) (json.RawMessage, error) {
		id++
		res := r.PostJSON(endpoint, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if res.Err != nil || res.Code != 202 {
			return nil, fmt.Errorf("POST %s %s: want 202: %s", endpoint, method, res)
		}
		for {
			d, err := next("message")
			if err != nil {
				return nil, fmt.Errorf("%s: %v", method, err)
			}
			var rpc struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(d), &rpc) != nil || rpc.ID != id {
				continue
			}
			if rpc.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, rpc.Error)
			}
			return rpc.Result, nil
		}
	}
	out, err := call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "tw-compat", "version": "1"}})
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), `"name":"example-server"`) {
		return fmt.Errorf("initialize: %s", out)
	}
	if res := r.PostJSON(endpoint, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); res.Code != 202 {
		return fmt.Errorf("notifications/initialized: %s", res)
	}
	if out, err = call("tools/list", map[string]any{}); err != nil || !strings.Contains(string(out), "check-product-stock") || !strings.Contains(string(out), "list-products") {
		return fmt.Errorf("tools/list: %s %v", out, err)
	}
	if out, err = call("tools/call", map[string]any{"name": "list-products", "arguments": map[string]any{}}); err != nil || !strings.Contains(string(out), "Products: Product 1, Product 2, Product 3") {
		return fmt.Errorf("list-products: %s %v", out, err)
	}
	if out, err = call("tools/call", map[string]any{"name": "check-product-stock", "arguments": map[string]any{"productName": "Product 1"}}); err != nil || !strings.Contains(string(out), "The product Product 1 is available in stock") {
		return fmt.Errorf("check-product-stock: %s %v", out, err)
	}
	r.Note("MCP over SSE: initialize, tools/list, list-products, check-product-stock")
	return nil
}
