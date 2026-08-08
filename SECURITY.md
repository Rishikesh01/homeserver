# Security policy

## Scope and threat model

Homeserver is designed for a **private home LAN**. It is not hardened for, and should not be
exposed to, the public internet — don't port-forward it, and don't put it in a DMZ.

Design assumptions:

- The LAN is the trust boundary; there is no remote access by design.
- The dashboard's admin area (`/admin`) includes a **root shell**. It is protected by a login
  and is bound only to loopback and the Docker bridge gateway — never the LAN interface
  directly — with Caddy in front.
- Data is stored unencrypted on disk. Full-disk encryption is the expected control against a
  stolen machine.

The full posture, including where each secret lives and what mode it's written with, is in
[docs/security.md](docs/security.md).

## Supported versions

This is a small project; fixes land on `main` and go out in the next tag. Please test against
the latest tag or `main` before reporting.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub:
[**Security → Report a vulnerability**](https://github.com/Rishikesh01/homeserver/security/advisories/new).
That opens a private advisory visible only to the maintainer.

Helpful to include:

- what an attacker can do, and what access they'd need to start (already on the LAN? an
  authenticated dashboard session? physical access?),
- the affected component (`hsctl`, the dashboard, Caddy config, a compose file, the backup path),
- steps to reproduce, and the version (`hsctl --version`),
- any suggested fix.

Expect an initial response within about a week. Once a fix is out, you'll be credited in the
advisory unless you'd rather not be.

## Out of scope

- Issues that require the stack to be deliberately exposed to the internet, which the
  documentation explicitly tells you not to do.
- Vulnerabilities in the upstream applications themselves (Vaultwarden, Nextcloud, Pi-hole,
  Stirling-PDF, IT-Tools, Caddy) — please report those to their own projects. If a pinned
  image here is on a known-vulnerable version, an issue asking for a version bump is welcome
  and is not a security report.
- The `/admin/terminal` shell being powerful. That's intentional and documented; a report that
  an authenticated admin can run commands isn't a vulnerability. A way to *reach* it without
  authentication very much is.
