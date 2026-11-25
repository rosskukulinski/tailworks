// e2e/dataplane cron loop, stateless workload: cron triggers only (no
// Durable Objects), so it holds a lease but no fence keeps it from serving
// through a partition. Only hostd's heartbeat-freshness rule (hostd/cron.go
// cronLeaseFresh) keeps a cut-off host from running its triggers.
import { scheduled } from "../../cron.js";

export default {
  async fetch() {
    return Response.json({ wl: "cron" });
  },
  async scheduled(controller, env, ctx) {
    await scheduled("cron", controller, env, ctx);
  },
};
