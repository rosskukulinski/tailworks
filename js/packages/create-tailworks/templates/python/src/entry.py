# python: a Python Worker (Pyodide) that greets its verified tailnet caller
# and counts their visits in a Durable Object. fetch() leaves through this
# Worker's own tailnet node (tag:tw-wl-python).
import json

from workers import DurableObject, Response, WorkerEntrypoint

from tailworks import caller_name, get_caller


class Counter(DurableObject):
    """One counter per name, in the object's SQLite-backed storage."""

    async def increment(self):
        n = (await self.ctx.storage.get("n") or 0) + 1
        await self.ctx.storage.put("n", n)
        return n


class Default(WorkerEntrypoint):
    async def fetch(self, request):
        who = get_caller(request)
        name = caller_name(who)
        visits = await self.env.COUNTER.getByName(name).increment()
        body = {
            "hello": name,
            "greeting": self.env.GREETING,
            "visits": visits,
            "caller": {k: who.get(k) for k in ("nodeId", "node", "tags", "user", "addrs")} if who else None,
        }
        return Response(json.dumps(body), headers={"content-type": "application/json"})
