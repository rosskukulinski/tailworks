---
kind: security
upgrade: []
---
A killed object can no longer be brought back with a new tailnet node by
code that was allowed to create it before the kill. A kill now ends every
permission to create that name: the host forgets any pending create
admission and any public-create record for it, and refuses new admissions
for the name (`410 killed`) until it is purged. A tenant's deletion forgets
all of the tenant's admissions. The runtime's per-isolate admit cache keeps
only "this object exists" answers, for at most 60 seconds, and a plain
Durable Object's kill clears its entry.
