// chat-agent: Cloudflare's chat agent example (AIChatAgent from
// @cloudflare/ai-chat) on tailworks. Each chat is a Durable Object and its
// own tailnet node; messages persist in its SQLite, streams resume on
// reconnect, and it hibernates when idle. It has the example's three kinds
// of tool:
//
//   getWeather       runs on the server
//   getUserTimezone  has no execute: the browser answers it (useAgentChat's onToolCall)
//   calculate        asks the user for approval first when a number is over 1000
//
// The model (src/model.ts) is an OpenAI-compatible gateway on your tailnet,
// such as Aperture, when wrangler.jsonc vars.LLM_URL is set, and a scripted
// mock (no keys) when it is empty. Tailworks has no Workers AI binding.
//
//   GET  /                                      the chat page (your own chat)
//   WS   /agents/chat-agent/<name>              the page, or useAgentChat (@cloudflare/ai-chat/react)
//   GET  /agents/chat-agent/<name>/get-messages the stored transcript
//   POST / (body: the message), GET /?message=… one turn over HTTP, in your own chat
//   GET  /messages                              your own chat's transcript
import { AIChatAgent } from "@cloudflare/ai-chat";
import { getAgentByName, routeAgentRequest } from "agents";
import { convertToModelMessages, pruneMessages, stepCountIs, streamText, tool, type UIMessage } from "ai";
import { z } from "zod";
import { getCaller, principalKey } from "@tailworks/tailnet";
import { model } from "./model";
import { page } from "./page";

// Env (the ChatAgent namespace) is the global interface `tw types` writes to
// worker-configuration.d.ts. Run `tw types` again after changing
// wrangler.jsonc or tailworks.jsonc.

export class ChatAgent extends AIChatAgent<Env> {
  async onChatMessage() {
    const result = streamText({
      model: model(this.env, this.ctx),
      system: "You are a helpful assistant. You can check the weather, get the user's timezone, and run calculations.",
      messages: pruneMessages({
        messages: await convertToModelMessages(this.messages),
        toolCalls: "before-last-2-messages",
      }),
      tools: {
        // Server-side tool: runs automatically on the server.
        getWeather: tool({
          description: "Get the current weather for a city",
          inputSchema: z.object({ city: z.string().describe("City name") }),
          execute: async ({ city }) => {
            // Replace with a real weather API (over the tailnet: ctx.tailnet.fetch).
            const conditions = ["sunny", "cloudy", "rainy"];
            return {
              city,
              temperature: Math.floor(Math.random() * 30) + 5,
              condition: conditions[Math.floor(Math.random() * conditions.length)],
            };
          },
        }),

        // Client-side tool: no execute function; the browser handles it.
        getUserTimezone: tool({
          description: "Get the user's timezone from their browser",
          inputSchema: z.object({}),
        }),

        // Approval tool: requires user confirmation before executing.
        calculate: tool({
          description: "Perform a math calculation with two numbers. Requires user approval for large numbers.",
          inputSchema: z.object({
            a: z.coerce.number().describe("First number"),
            b: z.coerce.number().describe("Second number"),
            operator: z.enum(["+", "-", "*", "/", "%"]).describe("Arithmetic operator"),
          }),
          needsApproval: async ({ a, b }) => Math.abs(a) > 1000 || Math.abs(b) > 1000,
          execute: async ({ a, b, operator }) => {
            const ops: Record<string, (x: number, y: number) => number> = {
              "+": (x, y) => x + y,
              "-": (x, y) => x - y,
              "*": (x, y) => x * y,
              "/": (x, y) => x / y,
              "%": (x, y) => x % y,
            };
            if (operator === "/" && b === 0) return { error: "Division by zero" };
            return { expression: `${a} ${operator} ${b}`, result: ops[operator](a, b) };
          },
        }),
      },
      stopWhen: stepCountIs(5),
    });
    return result.toUIMessageStreamResponse();
  }

  // HTTP, for curl and tests: a message runs one turn (saveMessages, the
  // SDK's server-side way to add a message and reply); without one, the
  // transcript. A browser client uses the WebSocket instead, which also
  // answers getUserTimezone and approvals.
  async onRequest(req: Request): Promise<Response> {
    const url = new URL(req.url);
    const text = req.method === "POST" ? await req.text() : url.searchParams.get("message");
    if (!text && !url.pathname.endsWith("/messages")) return new Response("not found", { status: 404 });
    if (!text) return Response.json({ chat: this.name, messages: this.messages });
    const msg: UIMessage = { id: crypto.randomUUID(), role: "user", parts: [{ type: "text", text }] };
    const { status } = await this.saveMessages((messages) => [...messages, msg]);
    const last = this.messages.at(-1);
    const reply = last?.role === "assistant" ? last : undefined;
    return Response.json({
      chat: this.name,
      status,
      reply: reply?.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("") ?? "",
      // tool parts: a client tool waits in "input-available", an approval in "approval-requested"
      parts: reply?.parts.filter((p) => p.type.startsWith("tool-")) ?? [],
    });
  }
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const routed = await routeAgentRequest(req, env);
    if (routed) return routed;
    // Anything else is the caller's own chat, keyed by their tailnet identity.
    // URL-encoded: routeAgentRequest takes the name from the path as-is, so
    // /agents/chat-agent/<name> (the page's WebSocket) reaches this same chat.
    const name = encodeURIComponent(principalKey(getCaller(req)));
    const url = new URL(req.url);
    if (req.method === "GET" && url.pathname === "/" && !url.searchParams.has("message")) {
      return new Response(page(name), { headers: { "content-type": "text/html; charset=utf-8" } });
    }
    const chat = await getAgentByName(env.ChatAgent, name);
    return chat.fetch(req);
  },
};
