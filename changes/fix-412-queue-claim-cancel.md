---
kind: fix
upgrade: []
---
A queue message is no longer lost for a visibility timeout when tw-hostd
stops a consumer at the moment mgmt claims it (#412). This happens on a
stale heartbeat, a self-fence, a redeploy or a shutdown. Before, mgmt
counted an attempt for a batch nobody received. The batch came back only
after the visibility timeout (60 s by default) with a retry used up, so a
message with `max_retries: 1` could reach its dead-letter queue without
ever running. Now, if the pulling request is gone when the claim commits,
or its answer can't be written, mgmt hands the batch back at once without
counting the attempt. A fenced host still claims nothing (#140).
