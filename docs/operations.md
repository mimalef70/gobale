# Operating GoBale

For installation and configuration, use the [README](../readme.md). Endpoint
contracts are in [OpenAPI](openapi.yaml); delivery behavior is in
[Webhook payloads](webhook-payload.md).

## Deployment and access

Run **one process per database and media directory**. SQLite uses WAL, FULL
synchronization and an OS ownership lock. Do not remove a running process's lock,
share its SQLite file over network storage or attach active replicas to one volume.

Administrative Basic Auth grants access to every connected account. The consuming
application must authorize its own users and organizations. Keep the listener
private or use TLS at the ingress. An explicit invalid device selector fails;
reusing a deleted alias does not transfer its old connection's data or work.

Sessions and stored secrets are encrypted with the deployment key. Ordinary
messages, webhook bodies and media are **not application-encrypted**. Protect the
data directory, logs and backups; use encrypted storage where required. Never
publish `.env`, `master.key`, rendered Compose configuration or database contents.

The Compose deployment binds the host port to localhost and stores SQLite/media
in a named volume under `/app/storages`. This is separate from a native process's
host `storages/` directory. Keep `APP_MASTER_KEY` stable across container recreation.
Host file paths are not container mounts; use an explicit Compose override when
changing key-file or storage locations. Restarting an existing container does
not apply changed Compose environment values; recreate it instead.

For a webhook receiver on the Docker host, use a reachable host address such as
`host.docker.internal`; container `127.0.0.1` refers to the container itself.
Administrator-configured webhooks may reach internal services. `/media/fetch`
instead rejects private/reserved destinations and redirects, and does not use
the configured HTTP proxies. These are intentionally different network policies.

## Backup, restore and upgrades

Keep a protected backup of the encryption key **separate from** database backups.
Generating a new key does not recover sessions encrypted by the previous key.

For a consistent cold backup:

1. Stop GoBale cleanly and confirm its process/container has exited.
2. Copy the complete storage directory or named volume, including the media tree.
3. Preserve the matching key and record the binary version and configuration.
4. Restore into an empty data directory with that key and start exactly one owner.
5. Check authenticated readiness, device status and required media access.

Never reconnect both the original and a restored copy of the same deployment.
The internal online database snapshot facility does not copy media and is not a
complete deployment backup. A normal file copy of a running WAL database is not
this cold-backup procedure.

New media references are relative to the configured root, so the complete tree
can move with a restore. Legacy absolute references must remain inside that root;
restore them at their original location or migrate their metadata explicitly.
Do not replace missing files with external symlinks.

Migrations run at startup; take a backup before upgrading. Foreign databases and
wrong master keys are rejected. Roll back with a compatible database snapshot,
not an arbitrary older binary against a newer schema. Check release notes for
protocol and storage changes before replacing the running version.

## Retention and disk use

This release retains data without an automatic TTL or background cleaner. It
does not promise bounded disk consumption or provide selective erasure commands.

| Data | Retention behavior |
| --- | --- |
| Messages, events and checkpoints | Retained with account-scoped history. |
| Webhook deliveries | Delivered, failed, paused and pending records remain; replay needs the original event. |
| Sends and schedules | Results, unknown outcomes and idempotency records remain. Removing them can allow an old key to send again. |
| Media and provider references | Retained; unresolved operations and schedules may still require them. |
| Sessions | Local logout or device deletion removes the encrypted session. |
| Deleted devices | Tombstoned; historical messages, files and audit rows remain under the old immutable connection ID. Deletion is not data erasure. |

Choose a disk budget and alert on growth/free space, leaving room for SQLite WAL,
backups and concurrent media transfers. Bounded worker queues do not bound retained
history. A disk-full persistence failure needs operator attention; GoBale must
not silently discard pending work or report successful persistence.

Do not run ad hoc DELETE statements or remove files from a live data directory.
Any planned retention migration must preserve sessions, connection IDs,
checkpoints, unresolved/failed work, deduplication/idempotency records and referenced
media. A future cleaner must not delete pending, failed or ambiguous work merely
because it is old.

For complete decommissioning, stop the service, prevent it from reconnecting,
and apply your erasure policy to the retired data directory, media, backups and
key. Provider session revocation and local data erasure are separate actions.

## Monitoring and lifecycle

`/health` is public liveness. Authenticated `/ready` checks storage; it does not
certify that every account has recovered. Per-device status separates `auth`,
`transport` and `recovery`. A connected socket with degraded recovery must not be
presented as a fully synchronized inbox. `APP_BASE_PATH` prefixes these routes too.

Authenticated `/metrics` exposes queue/worker measurements without message
content. Alert on unknown sends, oldest-pending age, failed deliveries, recovery
gaps, persistent reconnects, database errors and disk use. A failed destination
can accumulate ordered backlog even while other destinations continue working.

Disconnect/reconnect preserves queued work. Logout cancels unsent work and future
schedule occurrences, while ambiguous in-flight work and audit records remain.
Local logout is reported separately from confirmation of remote session revoke.
Graceful shutdown preserves retryable work; unexpected termination can require
reconciliation and repeated webhook delivery.

Current alpha limitations and verified scope are summarized in the
[README](../readme.md). Worker limits, synthetic benchmarks and two-account tests
must not be interpreted as a proven fifty-real-account deployment capacity.
