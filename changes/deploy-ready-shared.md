---
kind: fix
upgrade: []
---
`tw promote` and `tw rollback` now report `deploy_crashloop`, with the
host's detail lines and the `tw logs <workload>` hint, when the promoted or
rolled-back deployment starts and then keeps exiting. They used to wait out
`--wait` and report `identity_pending` with no cause. The three commands now
share one readiness loop, so they can't drift apart again.
