# Security policy

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/mimalef70/gobale/security/advisories/new).
If that form is unavailable, email the repository maintainer at
[mostafa.a.aut@gmail.com](mailto:mostafa.a.aut@gmail.com) with “GoBale security” in
the subject. Do not open a public issue containing an exploitable vulnerability.

Include the affected version, deployment conditions, impact, and a minimal
reproduction with synthetic data. Never send an active session, OTP, API password,
encryption key, customer database, raw capture, or private message. Arrange a safe
transfer with the maintainer if sensitive evidence is necessary.

Reports are reviewed on a best-effort basis. There is no guaranteed response time
or paid bug bounty. Coordinated disclosure gives maintainers time to reproduce a
problem and prepare a fix before public details are released.

## Supported versions

GoBale is currently a prerelease. Security fixes target the latest published
prerelease; older prereleases do not receive a separate maintenance branch. See
[releases](https://github.com/mimalef70/gobale/releases) for current versions and
release-specific limitations. No stable-support or security-audit claim is made.

## Deployment boundaries

- Administrative API credentials control **all** devices. Device selection is not
  tenant authorization; the consuming application must enforce tenant isolation.
- Use private ingress or TLS with access controls. Do not expose an unauthenticated
  reverse-proxy route or a shared administration password to end users.
- Keep the deployment encryption key separate from database backups. Session and
  signing secrets are encrypted; message history and media still require filesystem
  and backup protection.
- Use distinct webhook signing secrets, verify signatures, and deduplicate events.
  A webhook receiver is part of the trusted integration boundary.
- Protect account session files as account credentials. Log out compromised
  sessions and rotate affected deployment credentials.
- Updates to Bale's unofficial user-account protocol may invalidate compatibility;
  degraded recovery and unknown send outcomes must remain visible.

For non-sensitive deployment questions, use [the support guide](SUPPORT.md).
