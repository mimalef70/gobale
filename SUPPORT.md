# Getting help with GoBale

Start with the [README](readme.md), [OpenAPI contract](docs/openapi.yaml),
[webhook guide](docs/webhook-payload.md), and [operations guide](docs/operations.md).
See [release status](readme.md#release-status) for current support boundaries.

## Bugs

Use the [bug-report form](https://github.com/mimalef70/gobale/issues/new?template=bug-report.yaml).
Include the GoBale version, operating system or image digest, affected endpoint,
expected behavior, actual response code, and a small synthetic reproduction. Say
whether the issue survives a restart and whether it occurs with the `purego` build.

Remove credentials, phone numbers, account IDs, names, message bodies, signed URLs,
and file access hashes from logs and examples. Do not upload a database or session.
For delivery problems, report state transitions and error codes rather than
private webhook bodies. See [SECURITY.md](SECURITY.md) for sensitive reports.

## Features and integration questions

Use the [feature-request form](https://github.com/mimalef70/gobale/issues/new?template=feature-request.yaml)
for a proposed capability, or an ordinary issue for a focused integration question.
Describe the user-facing result and provide protocol evidence if available. A
feature in another client is a useful reference, but not proof that it works with
the current Bale service.

GoBale is an independent community project, sponsored by [MuChat](https://mu.chat).
It is not affiliated with or supported by Bale. Sponsorship does not provide an
individual support agreement or production SLA. Maintainer support is best effort.

Please do not use issues to request account recovery, mass unsolicited messaging,
account restrictions being lifted, or access to private account information.
