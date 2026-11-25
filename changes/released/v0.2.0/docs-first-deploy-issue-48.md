---
kind: fix
upgrade: []
---
Docs for a first deploy. The README quick start now grants callers access to the workload (without that grant, the `curl` hangs on any tailnet whose policy isn't allow-all), and `tw deploy` prints that grant next to the tagOwners lines it prints for new tags. New pages: `docs/troubleshooting.md` (a first deploy that doesn't answer, `identity_pending`, tagOwners after renaming an app, `peer_not_visible`, a `failed` deployment), `docs/concepts.md` (a glossary) and `docs/install-testing.md` (the Linux support matrix and measured timings, moved out of `docs/INSTALL.md`). INSTALL's intro is shorter, and its isolation notes and `tw dev` section come after the install steps.
