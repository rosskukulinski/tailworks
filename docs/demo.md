# The launch demo

`scripts/demo.sh` runs the same ten steps every time, on a real tailnet: an
MCP server and an agent are deployed, a device on the tailnet asks the agent
to call a tool, one grant is narrowed and the same call is denied, the grant
is put back, and the agent is killed and leaves the tailnet. This page says
how to run it, how to record it, and where the recordings go.

The recording at the top of the [README](../README.md) is
[`assets/demo/demo.gif`](assets/demo/demo.gif), made on a Tailscale tailnet
as described under [Record it](#record-it).

## What it shows

| Step | Command | What you see |
| --- | --- | --- |
| 1 | `tw status` | tailworks is up (`--up` runs `tw up` first if it isn't) |
| 2 | `tw init demo --template mcp`, `tw init demo-agent --template agent` | the two templates, unmodified |
| 3 | `tw policy <app> --apply --yes`, then two grants | the apps' tags; who may call the agent; which tools the agent may call |
| 4 | `tw deploy demo`, `tw deploy demo-agent` | both apps running, each on its own node |
| 5 | `curl http://<agent node>/agents/support-agent/<name>/tool?server=http://<mcp node>/mcp&name=refund` | the agent refunds an order through the MCP server: `"ok":true` |
| 6 | `tw ls` | the agent instance is a Durable Object with its own tailnet node |
| 7 | the grant's `tools` loses `refund` | the same call answers `cap_missing`; `lookup_order` still works |
| 8 | the grant is restored | the call works again |
| 9 | `tw kill <agent node>` | the node is gone from `tw ls`; the call answers `killed` |
| 10 | `tw audit <agent node>` | create, mint, ready and kill, hash-chained |

The call in steps 5 to 9 goes device → agent workload → the agent object,
and the object calls the MCP server from its own node. The MCP server
decides by the object's tag and the `example.com/cap/mcp` capability on its
grant. The agent template's `/tool` route and the MCP template's
`authorizeTool` do this; the demo adds no code to either.

## Run it

You need tailworks running on this machine (`tw up`), Go and this repo (the
grant editor is built from `internal/demogrant`), and `curl` on a device of
the tailnet. On Tailscale, the OAuth client `tw up` uses needs **Policy File
› Write**, and **Devices › Core › Write** so that step 9 also deletes the
device ([INSTALL.md](INSTALL.md#the-oauth-client)).

```sh
scripts/demo.sh --tailnet <your tailnet>            # asks before the first policy write
scripts/demo.sh --tailnet <your tailnet> --cleanup  # undeploy, remove the two grants
```

`--tailnet` is required and must be the tailnet `tw status` reports; the
script stops if they differ. `--yes` skips the question (needed without a
terminal). The header of `scripts/demo.sh` lists every option and
environment variable. The ones you are most likely to set:

- `DEMO_CALL`: the curl that runs on a tailnet device. The default is `curl`
  on this machine; `DEMO_CALL="ssh laptop curl"` calls from another one.
- `DEMO_CALLER_SRC`: who the caller grant lets in. The default is
  `autogroup:member`; name your own login to keep it narrow.
- `DEMO_PAUSE`: seconds to wait between steps. Unset, the script waits for
  Enter on a terminal.

### What it changes in the tailnet policy

- **Adds** `tagOwners` entries for `tag:tw-wl-demo`, `tag:tw-wl-demo-agent`,
  `tag:tw-demo-agent-support-agent` and `tag:tw-demo-agent-lookup-agent`.
  `tw policy --apply` writes them.
- **Adds two grants**, written by `internal/demogrant` because `tw` never
  writes who may call an app:

  ```hujson
  {"src": ["autogroup:member"], "dst": ["tag:tw-wl-demo-agent"], "ip": ["tcp:80"]},
  {"src": ["tag:tw-demo-agent-support-agent"], "dst": ["tag:tw-wl-demo"], "ip": ["tcp:80"],
   "app": {"example.com/cap/mcp": [{"tools": ["lookup_order", "refund"],
           "limits": {"refund": {"maxUsd": 50, "perDayUsd": 200}}}]}},
  ```
- **Changes one thing**: the second grant's `tools`, to `["lookup_order"]`
  in step 7 and back in step 8. If the script stops in between (an error,
  Ctrl-C, a closed terminal), its exit trap restores the grant.

Every write is read with the ETag, checked by the control plane, and written
with `If-Match`. Comments and all other rules stay as they are. These edits
are made with the Tailscale (or Headscale) API directly, so they are not in
`tw audit`.

`--cleanup` undeploys both apps, removes the two grants and deletes the app
directories. The `tagOwners` entries stay.

### Run it twice

Run it again as often as you like: it redeploys over the running apps and
makes a new agent instance each time.

After `--cleanup`, a run deploys `demo` and `demo-agent` again under the
same names.

## Try it on a throwaway tailnet

`scripts/demo-headscale.sh up` starts a private tailnet for the demo:
Headscale in Docker, a Tailscale client container named `laptop` as the
calling device, and a tailworks with its own state directory. It never
touches `~/.tailworks` or a real tailnet.

```sh
make deps build
scripts/demo-headscale.sh up
. /tmp/tw-demo-hs/env && scripts/demo.sh --yes
scripts/demo-headscale.sh down
```

`make demo-test` does this and checks the result: the refusals, a first
run, a rerun, an interrupted run (the grant must come back) and `--cleanup`.

## Record it

The assets and where they go:

| Asset | Path | Made by |
| --- | --- | --- |
| Terminal cast | `docs/assets/demo/demo.cast` | asciinema |
| README GIF, under 60 s | `docs/assets/demo/demo.gif` | agg, from the cast |
| MP4 for the blog post and social, about 90 s | `docs/assets/demo/demo-90s.mp4` | a slower cast, then ffmpeg |
| Screenshot: Tailscale admin console, Machines, the `tw-*` nodes | `docs/assets/demo/admin-console-machines.png` | by hand, during step 6 |
| Screenshot: tailworks admin console, the agent's object page | `docs/assets/demo/tw-console-object.png` | by hand, during step 6 |

Install the tools with `brew install asciinema agg ffmpeg`.

1. **Start clean**, so step 3 shows the policy being written: a tailnet
   where the demo has not run, or a new throwaway tailnet. After a
   `--cleanup` the tags are still in the policy and the app names are used
   up ([Run it twice](#run-it-twice)).
2. **Record the cast.** `DEMO_CLEAR=1` clears the screen before each step;
   `DEMO_PAUSE=4` gives a recording of about 55 seconds.

   ```sh
   asciinema rec --window-size 120x40 --title "tailworks launch demo" \
     -c "env DEMO_PAUSE=4 DEMO_CLEAR=1 scripts/demo.sh --tailnet <tailnet> --yes" \
     docs/assets/demo/demo.cast
   ```
3. **Scrub it before you commit it.** A cast is a text file. Nothing in it
   may name a real tailnet, a login or email address, a node key, a secret,
   or an address on your own network:

   ```sh
   grep -oiE '[a-z0-9-]+\.ts\.net|[a-z0-9._-]+@[a-z0-9._-]+|tskey[^" ]*|/(Users|home)/[^" ]*|[0-9]+(\.[0-9]+){3}' \
     docs/assets/demo/demo.cast | sort | uniq -c
   ```

   What to expect from a real tailnet: the tailnet name in every node URL,
   and `<login>@<hostname> (tw cli)` in the audit line of step 10 (the
   operator; `USER=demo` before `tw up` and `scripts/demo.sh` changes the
   login part). Replace them in the cast with `sed`, or record on the
   throwaway tailnet, where the names are `demo.tw.test` and `demo`.
   `make lint` runs `scripts/check-secrets.sh` over the committed files too.
4. **Make the GIF** and check its length and size (under 60 s, well under
   5 MB):

   ```sh
   agg --font-size 14 --idle-time-limit 5 docs/assets/demo/demo.cast docs/assets/demo/demo.gif
   ffprobe -v error -show_entries format=duration -of csv=p=0 docs/assets/demo/demo.gif
   ```
5. **Make the MP4.** Record a second, slower cast with `DEMO_PAUSE=8`
   (about 90 seconds), scrub it the same way, then:

   ```sh
   agg --font-size 16 --idle-time-limit 9 demo-90s.cast demo-90s.gif
   ffmpeg -i demo-90s.gif -movflags +faststart -pix_fmt yuv420p \
     -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" docs/assets/demo/demo-90s.mp4
   ```
6. **Take the two screenshots** while the script waits after step 6 (run it
   without `DEMO_PAUSE`, so it waits for Enter). In the Tailscale admin
   console, open Machines and search for `tw-`: the workload nodes and the
   agent's own node are devices. In the tailworks admin console
   (`http://tw-mgmt.<tailnet>.ts.net/ui/`, [ADMIN-UI.md](ADMIN-UI.md)),
   open the agent's object page. Crop out the tailnet name and your login,
   or blur them.

### The recording in this repo

`demo.cast` and `demo.gif` were recorded on 2026-11-24 on a Tailscale
tailnet, with the commands in steps 2 and 4 (`asciinema rec` also had
`--headless -f asciicast-v2`, and `DEMO_CALLER_SRC=group:admin` kept the
caller grant to that tailnet's admins). tailworks ran from a checkout with
its own state directory and a new OAuth client with Policy File › Write;
the calls are plain `curl` on the same machine, which is a device of that
tailnet. `tw kill` reports no posture tier because that tailnet's plan has
no custom posture attributes.

These strings in the cast were replaced before the GIF was made: the
tailnet name (now `example.ts.net`), the caller's login (`you@example.com`),
the host name in the audit line (`demo@host`), and a user ID and a key ID
(`uDEMOuser11CNTRL`, `kDEMOkey011CNTRL`). Nothing else was edited.

The screenshots and the MP4 are not in the repo yet.
