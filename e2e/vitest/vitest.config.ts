// make test-vitest: tailworks templates' plain Worker and Durable Object logic under
// @cloudflare/vitest-plugin (tests run inside workerd, per test file
// storage), with @tailworks/tailnet/testing for callers and ctx.tailnet.
// One project per template: each has its own wrangler.jsonc.
import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

const template = (name: string) => ({
  plugins: [cloudflareTest({ wrangler: { configPath: `../../js/packages/create-tailworks/templates/${name}/wrangler.jsonc` } })],
  // testTimeout: the first call into the mcp and refund-approval templates takes
  // 3.5-5s (cold module and Workflow start in workerd); vitest's default 5s timed out.
  test: { name, include: [`test/${name}.test.ts`], testTimeout: 30_000 },
});

export default defineConfig({
  test: {
    projects: [
      template("lock"),
      template("agent"),
      template("mcp"),
      template("refund-approval"),
    ],
  },
});
