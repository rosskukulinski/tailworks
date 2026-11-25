// The model: an OpenAI-compatible LLM gateway on your tailnet when
// wrangler.jsonc vars.LLM_URL is set, else a scripted mock.
//
// Aperture (Tailscale's AI gateway) is the intended gateway: set LLM_URL to
// "http://<aperture-hostname>/v1" and LLM_MODEL to a model Aperture serves.
// Pi's requests are a plain fetch() from this Durable Object, which on
// tailworks leaves from the assistant's own tailnet node, so Aperture sees,
// logs and authorizes each assistant by its node and tag
// (tag:tw-pi-agent-…). No API key: Aperture adds the provider's. Aperture is
// deny-by-default: its config needs a grant for that tag, and the tailnet
// policy needs one from the tag to Aperture (`tw policy` prints it when
// tailworks.jsonc "tailnet.connects" names Aperture's tag).
//
// The scripted mock (pi-ai's faux provider) runs with no gateway: a prompt
// with text in quotes calls word_count on it, "<a> × <b>" (or * or x) is
// answered directly, and after a tool result it answers from it.
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { createProvider } from "@earendil-works/pi-ai/models";
import { fauxAssistantMessage, fauxProvider, fauxText, fauxToolCall, type FauxResponseFactory } from "@earendil-works/pi-ai/providers/faux";

const text = (content: unknown): string =>
  typeof content === "string" ? content : Array.isArray(content) ? content.map((c: any) => c.text ?? "").join("") : "";

const respond: FauxResponseFactory = (context) => {
  const last: any = context.messages.at(-1);
  if (last?.role === "toolResult") {
    return fauxAssistantMessage(fauxText(`${last.toolName}: ${text(last.content)}`));
  }
  const prompt = text(last?.content);
  const quoted = prompt.match(/["“]([^"”]*)["”]/);
  if (quoted) return fauxAssistantMessage(fauxToolCall("word_count", { text: quoted[1] }), { stopReason: "toolUse" });
  const mul = prompt.match(/(-?\d+)\s*[×x*]\s*(-?\d+)/);
  if (mul) return fauxAssistantMessage(fauxText(String(Number(mul[1]) * Number(mul[2]))));
  return fauxAssistantMessage(fauxText(`You said: ${prompt}. Ask me to count the words in "some quoted text", or for 47 × 19.`));
};

/** The provider Pi opens with and the model new sessions use. */
export function chooseModel(env: Env) {
  if (!env.LLM_URL) {
    const faux = scripted();
    return { provider: faux.provider, model: faux.getModel() };
  }
  const model = {
    id: env.LLM_MODEL,
    name: env.LLM_MODEL,
    api: "openai-completions" as const,
    provider: "aperture",
    baseUrl: env.LLM_URL,
    reasoning: false,
    input: ["text" as const],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 128000,
    maxTokens: 8192,
  };
  const provider = createProvider({
    id: "aperture",
    baseUrl: env.LLM_URL,
    // Aperture ignores client keys and adds the provider's; the OpenAI client
    // still wants one, so this is a placeholder.
    auth: { apiKey: { name: "tailnet identity", resolve: async () => ({ auth: { apiKey: "aperture" }, source: "tailnet identity" }) } },
    models: [model],
    api: openAICompletionsApi(),
  });
  return { provider, model };
}

/** A faux provider whose one model answers every request with `respond`. */
function scripted() {
  const faux = fauxProvider({ provider: "tw-scripted", models: [{ id: "scripted" }] });
  // The faux provider answers from a queue; keep one `respond` step queued.
  const step: FauxResponseFactory = (...args) => {
    faux.appendResponses([step]);
    return respond(...args);
  };
  faux.setResponses([step]);
  return faux;
}
