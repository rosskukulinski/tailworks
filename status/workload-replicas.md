Last update: 2026-11-14 PT. Rebased onto master (smart placement, public ingress, containers, fast-identity); replicas now placed by ADR-033 scoring.

# Workload replicas behind one Service (branch `workload-replicas`)

Builds on PR #2 (Tailscale Services, `STATUS.md` "Tailscale Services for
workloads and mgmt replicas"). Design: [docs/services.md](../docs/services.md#replicas-one-stateless-workload-on-several-hosts).

## What's built

- **Config:** twd.jsonc `"replicas": N` (1..16) runs a **stateless** workload on N distinct hosts, usually behind one Tailscale Service (`tailnet.service`).
- **Model (`mgmt/workload_replicas.go`):** replica *i* is its own workload, `<workload>--r<i>`. Leases, epochs, deployment records, assignments, workload nodes and identity are therefore unchanged per replica. `"replicas": 1` is the old behavior.
- **Deploy flow:** `Deploy` routes the public call through `deployGroup`, which:
  - validates the request (`checkReplicas`);
  - **dry-places** every replica with smart placement (`m.place`, ADR-033), excluding hosts already chosen (new `placementInput.Exclude`), so too few hosts writes nothing;
  - deploys each replica through the normal `Deploy` body, so containers, public ingress, `--replace` and lease moves all apply per replica;
  - stops replicas past N.
- **Hooks in shared files** (AGENTS.md rule 4): `mgmt/service.go` gets one routing call at the top of `Deploy`, `r.replica.mark(classes)`, the Service owner (`r.replica.owner`), the `Exclude` passthrough, `v.Replicas = m.replicaView(...)`, and one field each on `DeployRequest`, `DeployResponse` and `DeploymentView`. `mgmt/placement_service.go` gets the `Exclude` field and one `continue`.
- **Refusals:**
  - a bundle with Durable Objects not marked `"fence": "never"` (`400 bad_bundle`, also at the dry-run preflight);
  - too few distinct hosts (`503 no_host`, nothing written);
  - `--host` combined with replicas;
  - deploying a replica by its own name, or a replica name taken by a separate workload (new `409 replica_conflict`).
- **Lifecycle:** a scale-down undeploys the extra replicas, and undeploying the workload undeploys all of them (`Undeploy`, `mgmt/service_v1.go`).
- **API:** `ClassConfig.replicaOf` / `.replicas`, `Deployment.replicas[]` (`ReplicaRef`). The CLI waits for every replica (`cmd/twd/replicas.go`) and prints `✓ replicas N · web@h1, web--r2@h2, …`.

## Test results

- **Hermetic:** `go test -run TestWorkloadReplicas ./mgmt/`, 9 cases with every request and response validated against the spec. After the rebase, `make lint` and `make test` (23 Go packages under `-race`, plus JS) both PASS. The rebase surfaced one semantic bug, now fixed: with `"service": true` the Service name defaulted to `svc:<replica name>`, so `web--r2` got `svc:web--r2`; it now comes from the group.
- **Real, before the rebase (2026-11-13), tailnet 1, `TestE2EReplicas` in `make e2e-svc`:**
  - host A came from `twd up`, host B was a second `twd-hostd` on the same Mac joined with `twd host add`;
  - `replicas: 2` placed on both hosts; the Service answered 5.6 s after deploy, caller = the client node;
  - 20 of 20 fresh connections from one client went to the same replica;
  - stalling that replica's workerd (SIGSTOP) made its host withdraw, and 5 consecutive answers came from the other replica 11.1 s later; it re-advertised 0.2 s after SIGCONT;
  - scaling to 1 removed the second replica and the Service kept answering; 0 leftovers.
- **Real, after the rebase (2026-11-14), same test, placement through ADR-033 scoring:** PASS. The Service answered 5.6 s after deploy; 20 of 20 fresh connections went to one replica; after the stall, the other replica answered steadily 9.6 s later; re-advertised 0.2 s after SIGCONT; scale-down and cleanup clean.

## Findings

- **Failover, not per-connection balancing:** Tailscale pins a client to one Service host. Load spreads across clients only.
- **Killed workload nodes:** redeploying a workload whose node was killed is refused (`bootstrap_failed: the workload node … is killed`) until `twd down --purge`. This is existing behavior, not caused by replicas; the e2e fails over by stalling workerd instead.

## Gaps and next steps

- **Cloudflare-style split for DO apps** (Worker replicated, each DO single-homed, stubs forwarded to the owning host): not started. It belongs on top of smart placement (deciding where a DO lives is a placement decision) and public ingress's `platform.js`. It needs a design doc first; call it `ADR-do-split` until it merges (AGENTS.md rule 5).
