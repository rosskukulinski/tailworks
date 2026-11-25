---
kind: fix
upgrade: []
---
An approval is now decided once (#561). Two decisions for the same
waiting approval that reached the host at about the same time (two
approvers, or one approver retrying a slow request) were both accepted:
both got `200`, both were delivered to the Workflow and both were audited
as `approval_decision`, even when one approved and the other rejected.
Now the first one wins and the other gets `409 approval_not_pending`
("is being decided"). If delivering the decision fails, the approval
stays waiting and can be decided again.
