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

## Administrative panel

`APP_UI_ENABLED=true` (default) serves the embedded panel at `/ui/`, including
`APP_BASE_PATH` when configured. The same artifact contains the UI and API; assets
are never downloaded at runtime. Missing, stale or corrupt assets fail startup
before database ownership or account startup. Rebuild the complete artifact, or
explicitly disable the UI for a Go-only/API-only deployment. UI metadata requires
no database migration; other changes in this release may still migrate storage.

Local browser access accepts only `localhost`/loopback Host values. For remote
access set `APP_UI_PUBLIC_ORIGIN=https://gateway.example.com` (no trailing slash
or path), terminate HTTPS at a trusted reverse proxy, and preserve that public
Host header when forwarding to the private GoBale listener. With a base path such
as `/bale`, open `https://gateway.example.com/bale/ui/`; the origin still excludes
`/bale`. Expose only the TLS ingress, not the internal plaintext listener.
Arbitrary `Forwarded`/`X-Forwarded-*` values do not grant browser access. Cookies
are Secure for this configured HTTPS origin; remote plaintext use is unsupported.
Forward the complete path, including `APP_BASE_PATH`, without stripping the prefix.
Do not cache `/ui/auth/` or `/ui/api/` responses. When a public origin is configured,
open the panel through that origin; a direct localhost URL is no longer accepted
by its Host check. Existing Basic API access is separate from this browser policy.

The panel exchanges existing Basic credentials for a random HttpOnly,
SameSite=Strict, path-scoped cookie. Origin and CSRF checks protect mutations.
Sessions are memory-only: at most 100, with 30-minute idle and eight-hour absolute
expiry. Polling counts as activity. Logout invalidates the panel session; restart
invalidates every panel session but preserves encrypted Bale sessions. Login has
per-IP and global limits; proxies can share the per-IP budget. Keep credentials
out of URLs and proxy logs. Only language/theme are saved in localStorage.

`/ui/api/` exposes the finite management subset under browser-session auth; cookies
do not authorize the public Basic API, and Basic alone does not authorize UI API.
All account-scoped UI requests require `X-Device-Instance`. A stale tab gets
`409 DEVICE_INSTANCE_CHANGED` if its alias now belongs to another connection.
External Basic clients can opt into this guard while existing clients remain valid.

Snapshots read local status and batch queue counts; polling does not call Bale.
Lists refresh every ten seconds, active login every three and delivery lists every
fifteen, while visible. Payloads are loaded on demand as escaped JSON. Retry targets
the previous destination at its original queue position; replay explicitly uses
current routing. Replacing only a webhook secret affects later attempts to the
same URL. An empty device URL inherits global routing; no global URL means disabled.

Keep the existing signing secret unless deliberately rotating it; the panel never
reads it back. A device destination requires a non-empty secret. Clearing its URL
switches to the configured global rules, including their event filters and secrets.
Changing a destination pauses its old pending work. Review paused/failed deliveries
before replay: replay can redeliver an event a receiver has already processed, so
the receiver must deduplicate by `event_id`.

### Panel troubleshooting

| Symptom | Action |
| --- | --- |
| `/ui/` is unavailable with a published older artifact | Build this checkout or install a release that includes the panel. UI files are not downloaded at runtime. |
| Startup reports missing, stale or corrupt UI assets | Run `make build` or rebuild the Docker image. For an intentional API-only deployment, set `APP_UI_ENABLED=false`; browser routes are then absent, while Basic API routes remain. |
| `UI_ORIGIN_REJECTED` | Check the exact HTTPS public origin, browser URL and forwarded Host. Keep the configured path prefix. Do not rewrite Origin or rely on forwarded headers to bypass the check. |
| `UI_UNAUTHORIZED` after a restart or idle period | Sign in to the panel again. This alone does not require logging the Bale account in again. |
| A pending Bale login returns to the phone step | A challenge expires or is lost on restart; request a fresh code explicitly. A page refresh can only resume a still-valid challenge in the same server process. |
| `DEVICE_INSTANCE_CHANGED` | Refresh the account list and reselect the intended connection; the old alias was replaced. Do not retry the stale action against the replacement. |
| `UI_LOGIN_RATE_LIMITED` or `AUTH_RESEND_TOO_SOON` | Wait for the indicated retry time. Refreshing the page or opening a second tab does not reset the server-side limit. |

Panel sign-out, Bale logout and connection deletion are different actions. The
latter two cancel unsent account work; neither promises to erase retained history,
media, ambiguous sends or audit records. See [Retention and disk use](#retention-and-disk-use).

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

Schema 5 adds durable webhook queue positions. Existing rows retain their
creation order (including row-order ties); new manual retries keep their original
position. A retry already reordered by an older version cannot be retrospectively
resequenced because that version recorded no retry-versus-replay lineage. Older
binaries cannot open this schema: rollback requires restoring the pre-upgrade
backup. Recovery gaps recorded by this version survive restart;
a gap lost by an older version cannot be reconstructed without provider evidence.

On startup, unresolved voice operations with an already-stored exact own-message
proof are reconciled without contacting Bale. Account, immutable connection, peer,
request ID, direction and proof date must match the reviewed rules; otherwise the
operation remains `unknown`. This does not resend messages, generate new webhook
deliveries or move recovery checkpoints. Failed repair transactions roll back.

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
