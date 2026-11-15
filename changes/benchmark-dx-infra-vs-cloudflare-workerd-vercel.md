---
kind: feature
upgrade: [cli-json, config-default]
---
`twd dev` prints each request with its `console.*` lines and the stack of any uncaught exception (`--json`: a new `request` event), and picks up edits within about 0.2 s. An unknown command suggests the nearest one (`twd tail` → `twd logs`), `--help` shows each flag's type, and `twd ls` shows why a deployment failed. `twd init` names `package.json` after the app and adds `npm run dev`. The admin console and `twd audit` render audit records the same way (no raw JSON); the approvals page has one comment box with Approve and Reject buttons. `twd docs search` ranks the guides above the API contract and the ADRs.
