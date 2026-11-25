// Test fixture for hostd/gen/clocks_test.go (ledger claim C-clocks,
// security/THREAT-MODEL.md "Side channels inside a workload's process"):
// what tenant code can see of time and shared memory under the generated
// config. Cloudflare's production runtime freezes Date.now() while code runs
// and has no SharedArrayBuffer; this records what the pinned open-source
// workerd does, from the Worker and from a facet object.
import { DurableObject } from "cloudflare:workers";

// cpuLoop spins for a fixed number of iterations (never on the clock, so a
// frozen clock can't make it run forever) and counts the distinct values
// each clock returned meanwhile. It stops early once performance.now() has
// moved 200 ms, so a slow machine doesn't stretch the test.
function cpuLoop() {
  const dates = new Set();
  const perfs = new Set();
  const t0 = performance.now();
  let minStep = Infinity;
  let last = t0;
  let x = 1;
  let iterations = 0;
  for (let i = 0; i < 20_000_000; i++) {
    x = (x * 1103515245 + 12345 + i) | 0;
    dates.add(Date.now());
    const p = performance.now();
    perfs.add(p);
    if (p > last) {
      minStep = Math.min(minStep, p - last);
      last = p;
    }
    iterations = i + 1;
    if (p - t0 > 200) break;
  }
  return {
    iterations,
    sink: x,
    elapsedMs: performance.now() - t0,
    dateNowDistinct: dates.size,
    performanceNowDistinct: perfs.size,
    performanceNowMinStepMs: minStep === Infinity ? null : minStep,
  };
}

async function probe() {
  const out = { clocks: cpuLoop() };
  out.sharedArrayBuffer = typeof SharedArrayBuffer;
  out.atomicsWait = typeof Atomics?.wait;
  try {
    const mem = new WebAssembly.Memory({ initial: 1, maximum: 1, shared: true });
    out.sharedWasmMemory = mem.buffer instanceof SharedArrayBuffer ? "shared" : "unshared";
  } catch (e) {
    out.sharedWasmMemory = "throws: " + String(e?.message ?? e);
  }
  try {
    const wt = await import("node:worker_threads");
    try {
      const w = new wt.Worker("while(true){}", { eval: true });
      await w.terminate();
      out.workerThreads = "started";
    } catch (e) {
      out.workerThreads = "throws: " + (e?.code ?? "") + " " + String(e?.message ?? e);
    }
  } catch (e) {
    out.workerThreads = "import throws: " + String(e?.message ?? e);
  }
  return out;
}

export class Clock extends DurableObject {
  async fetch() {
    return Response.json({ from: "facet", ...(await probe()) });
  }
}

export default {
  async fetch() {
    return Response.json({ from: "worker", ...(await probe()) });
  },
};
