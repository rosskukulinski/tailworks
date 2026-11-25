Last update: 2026-11-24 PT. The launch demo recorded again, with plain `curl` from a device of the tailnet (#323).

# Launch demo: third recording

Continues [demo-323-tailscale-recording](demo-323-tailscale-recording.md).
That recording was from the Tailscale test tailnet, where the caller had to
be a container (`docker exec laptop curl`). This one is from a Tailscale
tailnet the recording machine is itself a device of, so the calls are
plain `curl`.

## What was done

- `scripts/demo.sh --tailnet <tailnet> --yes` with `DEMO_PAUSE=4
  DEMO_CLEAR=1 DEMO_CALLER_SRC=group:admin` under `asciinema rec`: all ten
  steps, audit chain verified.
- tailworks was `bin/tw` from master with its own state directory under
  `/tmp`, `tw up --service=false --ephemeral --mgmt-hostname tw-demo-mgmt`,
  and a temporary OAuth client (auth_keys, devices:core, policy_file;
  `tag:tw`). The machine's installed tailworks was not used.
- `docs/assets/demo/demo.cast` and `demo.gif` replaced (59.2 s, 862 KB),
  scrubbed as `docs/demo.md` says; the scrub grep finds only
  `example.ts.net`, `you@example.com` and `demo@…` names.
- Afterwards: `scripts/demo.sh --cleanup`, `tw down --purge`, the four demo
  `tagOwners` entries removed again, the OAuth client and the two
  `tw-demo-mgmt` devices deleted by ID. The tailnet policy is byte for byte
  the backup taken before the first write.

## Takes that were thrown away

1. Step 5 failed: a stale `DEMO_CALL` from the previous recording's
   environment file pointed at a container that no longer existed. My
   mistake, not the script's.
2. All ten steps ran, but step 10 printed `chain NOT VERIFIED … host:demo
   BROKEN at 3 (prevHash does not link to the previous record)`. Between
   takes the instance had been stopped with `tw down --purge` and started
   again with `tw up` on the same state directory. A fresh state directory
   gave a verified chain. Filed as an issue; see the PR.

## Tests

- The recorded run and its cleanup, as above.
- `make lint`, `go test -count=1 ./docs/...`, `make changes-check`: see the PR.
