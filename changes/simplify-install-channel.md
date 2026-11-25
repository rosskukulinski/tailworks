---
kind: feature
upgrade: [cli-json]
---
`tw version --check`, `tw doctor` and `tw upgrade --plan` print the same
upgrade command, chosen by how this `tw` was installed: `tw upgrade` for an
`install.sh` prefix, `tw backup && tw down && npm install -g
tailworks@<version> && tw up` for a global npm install, `npx
tailworks@latest …` for npx, `git pull && make build` in a source checkout,
and the `get.sh` line otherwise. Before, `tw version --check` said
`tw upgrade` for a source checkout or a copied binary, where it then failed.
On an npm install, `tw upgrade --plan` now prints the npm command instead of
failing; `tw upgrade` itself still refuses with `npm_install`.

After a healthy upgrade, `tw upgrade` runs the new `tw` to refresh the agent
skills an older `tw agent setup` wrote in your home directory (only files
and blocks `tw` wrote; nothing new is created), lists that step in
`--plan`, and reports it as `agentSkills` in `--json`. Project-scope skills
still need `tw agent setup --scope project` re-run in the project.
