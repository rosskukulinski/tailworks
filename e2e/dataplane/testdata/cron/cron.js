// The e2e/dataplane cron workloads' shared scheduled() (LOOP.md "Cron
// triggers and alarms", issue #419). Every run reports to the recorder node
// (env.REC), so the record survives whatever happens to the hosts:
//
//   POST /cron?wl&cron&st&run&phase&epoch   phase: start | tick | end
//
// st is controller.scheduledTime, run a random ID per run, epoch the lease
// epoch the run's host holds. The recorder notes the receipt time and the
// reporting node (WhoIs), which names the host.
//
// Three kinds of trigger (the expressions differ, the schedules mostly
// don't: the platform rejects duplicate expressions):
//
//   LONG  every run holds 75 s in scheduled() itself, so the next minute
//         comes due while it is in flight and must be skipped (no overlap).
//   WAIT  on minutes divisible by 3 the handler returns at once and leaves
//         70 s of work in ctx.waitUntil(), which still counts as in flight.
//   any other trigger returns at once.
//
// A held run reports a tick every 5 s, so a run that keeps going on a host
// that should have stopped it is caught acting.

export const LONG = "*/1 * * * *";
export const WAIT = "0-59 * * * *";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export async function epochOf(ctx) {
  try {
    return (await ctx.tailnet.self()).epoch;
  } catch {
    return 0;
  }
}

// report posts to the recorder and says whether it got there.
export async function report(rec, path, params) {
  try {
    const r = await fetch(rec + path + "?" + new URLSearchParams(params), { method: "POST" });
    await r.body?.cancel();
    return r.ok;
  } catch {
    return false;
  }
}

export async function scheduled(wl, controller, env, ctx) {
  const epoch = await epochOf(ctx);
  const run = crypto.randomUUID().slice(0, 12);
  const rep = (phase) => report(env.REC, "/cron", { wl, cron: controller.cron, st: controller.scheduledTime, run, phase, epoch });
  await rep("start");
  const hold = async (ms) => {
    for (let left = ms; left > 0; left -= 5000) {
      await sleep(Math.min(5000, left));
      await rep("tick");
    }
    await rep("end");
  };
  if (controller.cron === LONG) return hold(75_000);
  if (controller.cron === WAIT && Math.floor(controller.scheduledTime / 60_000) % 3 === 0) {
    ctx.waitUntil(hold(70_000));
    return;
  }
  await rep("end");
}
