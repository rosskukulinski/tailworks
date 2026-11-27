Last update: 2026-11-26. Fix the two nightly suites that broke after the 2026-11-25 agent-ready wave (#663).

Neither suite runs on PR CI, so each PR was green alone and the nightly was
the first to run the merged tree.

- **e2e-headscale `TestTwUp`:** #640 added `tw init … --template hello
  --keep-tags` on a branch cut before #628 removed `--keep-tags` (#526). The
  merge had no textual conflict, and a runtime flag is invisible to a build
  check. The flag was a no-op for `hello` (the test already expects
  `tag:tw-wl-callers`, which `tw init` produces), so it is dropped.
- **test-node `TestNodeWorkflow`:** the test asserted that the MCP server's
  `cap_missing` denial text contains the workflow's node ID, which came
  from the `chain` detail #621 removed on purpose (#471: hide unbuilt
  surface). The denial still names the caller by workload and tag
  (`a-node-workflow (tag:tw-wl-node-workflow)`), so the test asserts on
  that; the intent (the server authorized the deployment's own node, not
  the host) is unchanged. Nothing else in the repo read `chain`.

Checks:

- `make e2e-headscale`: PASS (TestPolicyProfile, TestNodeLifecycle,
  TestTwUp, TestRefusesOldHeadscale; 83 s, Headscale v0.29.7 in Docker).
- `make test-node`: PASS (TestNodeTier, TestNodeWorkflow,
  TestNodeTierSharedHostRefused).
- `go vet -tags e2eheadscale ./e2e/headscale/`, `go vet -tags nodetier
  ./e2e/node/`, `gofmt`: clean.
- `make lint`, `make changes-check`: see the PR.
