# Data-plane follow-up loops: 10-minute soaks

2026-11-25, macOS, seed 2001: one 10-minute soak of each follow-up loop's
widest scenario (workflows-move, cron, alarms, r2-s3, deploys-mixed on 3
Postgres mgmt replicas, routing, containers mixed on vz). All pass; results
are in `e2e/dataplane/LEDGER.md` "10-minute soaks of the follow-up loops".

## Tests
- The seven soaks above: pass.
- `make lint-docs test-docs`: see the PR.
