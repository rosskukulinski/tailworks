Last update: 2026-11-25. Two scaffold-naming fixes, so `tw init` and `npm create tailworks` (without tw) keep producing identical trees.

- **`appTags` renames in one pass** (`cmd/tw/inittags.go`). It used to run
  `bytes.ReplaceAll` once per renamed tag, in map order. When one rename's
  result was another rename's input (workload `a`, class `B`, template tags
  `tag:tw-wl-b` and `tag:tw-wl-a`), the order decided whether the
  workload's tag was renamed a second time into the class's. It now walks
  the quoted `"tag:tw-…"` strings once and looks each up in the rename map.
  `TestAppTagsRenamesOnce` runs the swap case 20 times.
- **`workloadLabel` in `init.mjs` lowercases İ like Go.** Go's
  `strings.ToLower` maps U+0130 to `i`; JavaScript's `toLowerCase` gives `i`
  plus a combining dot, which became a stray `-`. `İstanbul_App` is now
  `istanbul-app` on both sides. `TestCreateTailworksRenamesLikeInit` runs
  every template with that name as well as `My_App-2`.

Both bugs were found by PR #618 from an outside contributor, closed in
favour of this in-house change.

Checks: see the PR's Tests section.
