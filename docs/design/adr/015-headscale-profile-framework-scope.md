# ADR-015: Headscale profile and framework scope

**Date:** 2026-11-07
**Status:** Accepted.
**Decision:** The Headscale profile uses pre-auth keys, has no posture and no Services, and puts caps in grants only. MVP framework adapters are the Cloudflare Agents SDK and the MCP TS SDK. Claude Agent SDK, LangGraph and OpenAI Agents are tailnet *clients* of tailworks tools, not in-isolate.

**Implementation (Headscale profile, 2026-11-15).** Mint mode `headscale`. Run it with `tw up --control=headscale` or `tw tenant add --control=headscale`. User docs are in [headscale.md](../../headscale.md). The Headscale API client is `internal/headscale`, the policy profile `policy.Headscale` with `Options{Headscale: true}`, and mgmt's gates are in `mgmt/headscale.go`. The suite is `make e2e-headscale`, against pinned v0.29.7 in Docker. Findings against a real server:
- **Minimum v0.29.** v0.28's parser rejects `grants`, so it can't carry caps. Tailworks refuses older servers before minting.
- **Headscale rejects `autogroup:admin` as an owner.** tailworks' tags are therefore owned by `tag:tw`, and `tag:tw` has no owners.
- **The policy must define a tag before a node with it registers,** so `tw up` applies the snippet first.
- **A pre-auth-key registration that advertises tags is refused.** On this profile tailworks' nodes take their tags from the key only.
- **The API mints any tag regardless of `tagOwners`,** so the API key is the trust boundary.
- **A node-side rename reaches MagicDNS in about 1.2 s,** so the ADR-031 pool works.
- **A node's StableNodeID is its decimal ID,** so kill Tier 2 is `DELETE /api/v1/node/{id}`.
