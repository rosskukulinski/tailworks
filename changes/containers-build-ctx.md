---
kind: feature
upgrade: [config-default]
---
Containers: `tw deploy` honours wrangler's `image_build_context` and `image_vars` (and `build_context`/`build_vars` on a named image): the build context, and `--build-arg`s (their values are not echoed). Before, the context was always the Dockerfile's directory and the vars were dropped silently. Other `containers` keys tailworks ignores (`rollout_*`, `constraints`, …) now get one warning line each. `tw doctor` has a `containers` row saying whether this host offers containers and, if not, why; `tw up`'s tier line says `, containers` or `; containers off: see tw doctor`; `tw deploy`'s `no_host` (no host to build images for) lists the live hosts and their tiers instead of suggesting `tw host setup --microvm`; and `tw deploy` checks `docker buildx version` before building.
