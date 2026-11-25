---
kind: security
upgrade: []
---
Smart placement bounds what a host's path report can claim. A huge `rttMs`
or `ageMs` used to overflow, so a host could make its own path look like the
best one, or keep a measurement fresh for ever. mgmt now clamps a reported
RTT to 1 minute and an age to 1 hour, and placement ignores stored
measurements outside those bounds. `docs/security-model.md` now says what a
compromised host's path reports can and can't do (ledger H23).
