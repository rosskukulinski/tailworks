---
kind: feature
upgrade: [cli-json, config-default]
---
`tw dev` prints each request with its `console.*` lines and the stack of any uncaught exception (`--json`: a new `request` event), and picks up edits within about 0.2 s. An unknown command suggests the nearest one (`tw tail` → `tw logs`), `--help` shows each flag's type, and `tw ls` shows why a deployment failed. `tw init` names `package.json` after the app and adds `npm run dev`. The admin console and `tw audit` render audit records the same way (no raw JSON); the approvals page has one comment box with Approve and Reject buttons. `tw docs search` ranks the guides above the API contract and the ADRs.
