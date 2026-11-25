Last update: 2026-11-24. Converted container image cache capped at 20 GiB by default (#476).

## What changed

- `hostdrun.DefaultImageCacheMiB = 20480` is the default of
  `tw-hostd -container-image-cache-max-mib` and is passed explicitly by
  `tw up` (cmd/tw/up.go). `hostdrun.Options` zero still means no cap, as
  for the other container knobs; `0` on the flag keeps "no cap".
- The evict-before-Hold window is closed (hostd/image/hold.go):
  `Store.GetHeld` / `GetPinnedHeld` hold the image before GC runs, so GC
  never removes the image it was run for. If a concurrent start's GC removed
  it between conversion and the hold, the start converts it again (up to 3
  tries). `hostd/containers.go` uses them instead of Get + Hold. `Get` /
  `GetPinned` keep their signatures (hold, GC, release).
- docs/limits.md row and docs/containers.md paragraph updated;
  `changes/image-cache-cap.md` (`config-default`).

## Judgment call: no floor at the per-image cap

The issue says to floor the cache cap at the per-image cap. The per-image
default is 40 GiB, so a floor would make the effective default 40 GiB, not
the stated 20 GiB. The floor's purpose (a cap smaller than one image must
not break starts) is covered by the hold-before-GC rule: the image just
produced and every image in use stay, even past the cap, and the cache only
drops back under the cap once they are released. Not adding the floor keeps
the visible default honest.

## Tests

- `go test -count=1 ./hostd/image/` ok: new TestCacheCapHonoured,
  TestCacheCapBelowOneImage, TestCacheHeldPlusNewAboveCap,
  TestHeldRetriesAfterEviction, plus the existing TestCacheGC.
- `go test -count=1 ./internal/hostdrun/ ./docs/...` ok;
  `-run 'Container|Ctr|Image|Limits|Docs' ./hostd/ ./cmd/tw/` ok.
- `make cli-docs`: no change (docs/cli.md doesn't list tw-hostd flags).
- `make changes-check`: ok (no upgrade-relevant paths).
- `make lint test` (load ~80): lint passed; tests passed except 4 mgmt
  tests (TestRedeployAfterUndeploy, TestAPIIdentityScope,
  TestWarmKeysRevokedOnCloseInParallel, TestWarmKeyRefillRespectsThrottle)
  and 2 hostd tests (TestR2Streaming, TestWorkersKV) that timed out under
  load; all six pass rerun on their own (`go test -run ... ./mgmt/`,
  `./hostd/`). None touches the image cache.
- Skipped: `make e2e-upgrade` (changes-check: no upgrade-relevant paths);
  container e2e suites (need KVM / Mac virtualization with a container
  kernel).
