# ADR-014: Local dev evaluates real policy; outbound dependencies are declared

**Date:** 2026-11-07
**Status:** Accepted.
**Context:** DX-C1: testcontrol allows all traffic. DX-C2: a denied peer is simply invisible, so the call fails with "no such host".
**Decision:** `tw dev` embeds a policy evaluator (a candidate is Headscale's policy v2; license check pending). Config declares `tailnet.connects`. netd returns `peer_not_visible` with the missing grant and a snippet, and never falls back to the system resolver.
