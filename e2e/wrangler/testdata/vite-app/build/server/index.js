import { greet } from "./assets/greet-B1x2c3.js";
export default {
  async fetch(request, env) {
    const asset = await env.ASSETS.fetch(new URL("/hello.txt", request.url));
    return Response.json({ greeting: greet(), asset: (await asset.text()).trim() });
  },
};
