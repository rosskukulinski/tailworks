---
kind: feature
upgrade: [cli-json, config-default]
---
`tw init` now presents four starting points, `hello` (the default),
`agent`, `mcp` and `static-site`, in its help, `tw help` and the README.
The other templates (`ai-agent`, `refund-approval`, `lock`,
`node-workflow`, `python`) are demos, patterns and other runtimes; they
are listed by the new `tw init --list` (with `--json`: `{"templates":
[{name, purpose, needs, guide, starting_point}]}`) and linked from their
guides. Every template name is still accepted by `--template`.

After creating an app, `tw init` (and `npm create tailworks` without `tw`
installed, in its `npx tailworks` form) prints the same two next steps
for every template: `cd <dir> && tw dev` to try it here with no tailnet,
then `tw deploy --apply-policy`, which shows the policy lines it needs,
asks, writes them and deploys. `node-workflow` still runs `npm install`
first, and `ai-agent` still says its tools need the `mcp` template.
