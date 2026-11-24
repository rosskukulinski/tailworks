---
kind: feature
upgrade: []
---
Two new templates follow Cloudflare's Agents examples:
`tw init <dir> --template chat-agent` is the
[chat agent](https://developers.cloudflare.com/agents/examples/chat-agent/)
(`AIChatAgent` with a server tool, a browser tool and an approval tool),
and `--template pi-agent` is the
[Pi harness](https://developers.cloudflare.com/agents/harnesses/pi/)
(`PiHarness`: Pi Durable in a Durable Object). Each runs one agent per
caller on its own node. `chat-agent` serves a chat page at `/`.

Neither needs Workers AI: set `vars.LLM_URL` to
`http://<aperture-hostname>/v1` to use Aperture (or any OpenAI-compatible
gateway on your tailnet), and Aperture sees each chat or assistant as its
own node, with no API key in the app. With `LLM_URL` empty they use a
scripted model, so they run with no keys.

The Cloudflare Agents SDK (`agents`) is now 0.34.0 (was 0.33.0), which Pi
needs. The `agent`, `mcp` and `refund-approval` templates pin it too; an
app made from them keeps its own pin until you change it.
