// The model: an OpenAI-compatible LLM gateway on your tailnet when
// wrangler.jsonc vars.LLM_URL is set, else a scripted mock.
//
// Aperture (Tailscale's AI gateway) is the intended gateway: set LLM_URL to
// "http://<aperture-hostname>/v1" and LLM_MODEL to a model Aperture serves.
// Requests leave from this chat's own tailnet node (tailnet(ctx).fetch), so
// Aperture sees, logs and authorizes each chat by its node and tag
// (tag:tw-chat-agent-…): no API key in the app. Aperture is deny-by-default:
// its config needs a grant for that tag, and the tailnet policy needs one
// from the tag to Aperture (`tw policy` prints it when tailworks.jsonc
// "tailnet.connects" names Aperture's tag).
//
// For another OpenAI-compatible gateway that wants a key, keep the key a
// secret (`tw secret put LLM_API_KEY`; `.dev.vars` for `tw dev`) and pass
// apiKey: env.LLM_API_KEY below.
//
// The scripted mock (AI SDK `MockLanguageModelV4` from `ai/test`) runs with no
// gateway: "weather in <city>" calls getWeather, "timezone" calls
// getUserTimezone, "<a> <op> <b>" calls calculate, and after a tool result it
// answers from it.
import { createOpenAICompatible } from "@ai-sdk/openai-compatible";
import { tailnet } from "@tailworks/tailnet";
import { MockLanguageModelV4 } from "ai/test";
import { simulateReadableStream } from "ai";

const usage = {
  inputTokens: { total: 10, noCache: 10, cacheRead: 0, cacheWrite: 0 },
  outputTokens: { total: 10, text: 10, reasoning: 0 },
};
const finish = (unified: "stop" | "tool-calls") => ({ type: "finish", finishReason: { unified, raw: unified }, usage });

function textChunks(text: string): any[] {
  // Several deltas, so the reply visibly streams.
  const deltas = text.match(/[\s\S]{1,24}/g) ?? [text];
  return [
    { type: "text-start", id: "t0" },
    ...deltas.map((delta) => ({ type: "text-delta", id: "t0", delta })),
    { type: "text-end", id: "t0" },
    finish("stop"),
  ];
}

function toolCall(toolName: string, input: Record<string, unknown>): any[] {
  return [{ type: "tool-call", toolCallId: `call-${crypto.randomUUID().slice(0, 8)}`, toolName, input: JSON.stringify(input) }, finish("tool-calls")];
}

function script(prompt: any[]): any[] {
  const last = prompt.at(-1);
  if (last?.role === "tool") {
    const lines = last.content
      .filter((c: any) => c.type === "tool-result")
      .map((c: any) => `${c.toolName}: ${JSON.stringify(c.output?.value ?? c.output)}`);
    return textChunks(lines.join("\n") || "Done.");
  }
  const text: string = (last?.content ?? []).filter((c: any) => c.type === "text").map((c: any) => c.text).join(" ");
  const weather = text.match(/weather in ([\p{L} .'-]+?)[?.!]*$/iu);
  if (weather) return toolCall("getWeather", { city: weather[1].trim() });
  if (/time ?zone/i.test(text)) return toolCall("getUserTimezone", {});
  const calc = text.match(/(-?\d+(?:\.\d+)?)\s*([-+*/%x×])\s*(-?\d+(?:\.\d+)?)/);
  if (calc) {
    const operator = calc[2] === "x" || calc[2] === "×" ? "*" : calc[2];
    return toolCall("calculate", { a: Number(calc[1]), b: Number(calc[3]), operator });
  }
  return textChunks(`You said: ${text}. Ask about the weather in a city, your timezone, or a sum like 47 * 19.`);
}

export function model(env: Env, ctx: DurableObjectState) {
  if (env.LLM_URL) {
    const gateway = createOpenAICompatible({ name: "aperture", baseURL: env.LLM_URL, fetch: tailnet(ctx).fetch });
    return gateway(env.LLM_MODEL);
  }
  return new MockLanguageModelV4({
    modelId: "tw-scripted-chat",
    doStream: async (opts: any) => ({
      stream: simulateReadableStream({ chunks: script(opts.prompt ?? []), chunkDelayInMs: 5 }),
    }),
  });
}
