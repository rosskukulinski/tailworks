Last update: 2026-11-24 23:55 PT. A held-up deploy no longer lowers a workload's epoch (#584).

# fix-584-epoch-regress

## The bug

Found by the e2e/dataplane deploys loop (#421, `deploys-mgmt` seed 1, V1):
one Durable Object copy on h1 answered at epochs 144–147, then 145 again.
A deploy of epoch 145 hung fetching its bundle through a mgmt replica whose
Postgres link was blackholed; meanwhile `wantDeploy` raised the workload's
epoch to 147. When the held-up deploy resumed, `deploy` set
`w.epoch = a.Epoch` unconditionally, so the epoch (and with it
`ctx.tailnet.self().epoch`, the base of the lock template's fencing tokens)
went down until the next deploy ran.

## The fix

`deploy` only raises `w.epoch`, as `wantDeploy`, `revoke` and
`refreshEpoch` already did.

## Tests

- `TestDeployNeverLowersEpoch` (hostd): a newer assignment, then a
  held-up deploy of an older one; the epoch seen at its secret grant and
  after must stay 147. Fails before the fix (145), passes after.
- `go test ./hostd/ -run 'Deploy|Lease|Epoch|StallFence'`: pass.
