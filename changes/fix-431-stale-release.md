---
kind: fix
upgrade: []
---
Queues: when a consumer's workload moves (`tw deploy --replace`, a
takeover, a revoke) while its old host holds a batch it hasn't run yet,
the old host's hand-back is now accepted. The batch is visible again at
once and keeps its attempt count. Before, mgmt refused the hand-back
(`lease_lost`), so the batch waited out the visibility timeout and came
back with an attempt it never ran. With a low `max_retries`, a message
could reach its dead-letter queue having run fewer times than allowed
(#431). Only a release is accepted from the older epoch. Acks, retries
and extends from a host that lost the lease are still refused.
