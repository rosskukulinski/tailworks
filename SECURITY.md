# Security policy

## Reporting a vulnerability

Please report security problems privately. Don't open a public issue,
pull request or discussion for them.

Use GitHub's private vulnerability reporting: on the repository's
**Security** tab, choose **Report a vulnerability**
(<https://github.com/rosskukulinski/twd/security/advisories/new>). The
report reaches the maintainers only.

Include what you can of:

- the twd version (`twd version`) and the platform (macOS or Linux, tier:
  process or microVM);
- what an attacker needs (tailnet member, tenant code, a host, the public
  internet) and what they gain;
- steps to reproduce, or a proof of concept.

What happens next:

- We acknowledge the report, work on a fix in a private advisory, and may
  ask you to check it.
- The fix ships in a release whose notes have a **Security** section
  ([CHANGELOG.md](CHANGELOG.md)), and the advisory is published with it.
  We credit you unless you'd rather not be named.
- Security fixes go to the latest minor release and the one before it
  ([docs/RELEASING.md](docs/RELEASING.md#branches)).

twd is pre-1.0, maintained on a best-effort basis, with no bug bounty.

## Supported versions

| Version | Security fixes |
|---|---|
| the latest minor (0.N) | yes |
| the minor before it (0.N−1) | yes |
| older | no: upgrade ([docs/upgrading.md](docs/upgrading.md)) |

## What's in scope

The twd binaries (`twd`, `twd-mgmt`, `twd-hostd`, `twd-netd`,
`twd-vmhelper`), the microVM guest, the platform runtime and the `@twd/*`
packages in this repository. Bugs in workerd, Firecracker or Tailscale go
to those projects; tell us too if twd's use of them makes things worse.

Known limits are not vulnerabilities, but tell us if one is worse than
documented. Read these first:

- [docs/security-model.md](docs/security-model.md): identity, isolation
  tiers and trust boundaries, on one page;
- [security/THREAT-MODEL.md](security/THREAT-MODEL.md) and
  [security/RED-TEAM.md](security/RED-TEAM.md): the red team's model and
  findings;
- [security/LEDGER.md](security/LEDGER.md): open hypotheses and which
  findings are fixed.
