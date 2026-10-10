# Upgrade from GoBale to GoOmni 2.3

GoOmni continues the same gateway and Git history. It adds native Eitaa and
Rubika adapters beside Bale, with one binary, API and administrative panel.
Consumers retain their own users, permissions and channel ownership mappings.
This is a coordinated gateway/client upgrade, not a second API or a legacy alias.

## Names and contracts

| Item | GoOmni 2.3.0 |
| --- | --- |
| Repository and Go module | `mimalef70/goomni`, `github.com/mimalef70/goomni/src` |
| Binary and CLI | `goomni` |
| Docker Hub | `docker.io/mimalef70/goomni:v2.3.0` |
| GHCR | `ghcr.io/mimalef70/goomni:v2.3.0` |
| Webhook identity headers | `X-GoOmni-Event-Id`, `X-GoOmni-Delivery-Id` |
| Webhook signature | `X-Hub-Signature-256`; the existing HMAC-SHA256 algorithm |
| Prometheus metric prefix | `goomni_` |
| Administrative session cookie | `goomni_admin`; sign in again after upgrading |
| New-install database / Docker volume | `storages/goomni.db` / `goomni-data` |

**Keep the original database filename, media directory, volume and master key
when upgrading.** A different filename or volume can create an empty installation.
There is no need to rename `gobale.db`. Existing connection IDs, instance guards,
sessions, provisioning keys, operation IDs/order, schedules and media remain valid.
Internal cryptographic labels, metadata tables, media locks and staging ownership
names intentionally retain their original values; they are part of stored data.

Application version 2.3.0 and storage schema 8 identify different things. Startup
transactionally upgrades a schema-7 GoBale database and assigns `provider: bale`
to all its connections, including deleted ones. A schema version prevents an
incompatible binary from opening the data; it does not imply parallel APIs.
Restoring an older binary requires its matching pre-upgrade snapshot.

## Update the consuming backend

Use the [consumer contract](consumer-integration.md), [OpenAPI](openapi.yaml) and
[webhook guide](webhook-payload.md) shipped with this release.

1. Keep the existing backend credential and each connection's saved `instance_id`.
   Account-scoped requests still need `X-Device-Id` (or a device route) and
   `X-Device-Instance`. A stale instance must not automatically bind to a new account.
2. Include an explicit `provider` (`bale`, `eitaa` or `rubika`) in new
   `POST /devices` requests and retain stable provisioning idempotency keys.
   Existing Bale connections need no recreation. Provider cannot change later.
3. Discover enabled adapters with `GET /app/providers`, their limits and
   interactions, then read `GET /app/capabilities?provider=…` or the guarded
   `GET /devices/{id}/capabilities`. Eitaa/Rubika are opt-in; unsupported operations
   fail before enqueue. Use their own history cursors and opaque string IDs.
4. Read `provider` in new event envelopes and route by immutable connection,
   provider and peer. Ordinary send/upload/schedule/operation flows are shared.
   Apply sparse edits only to the fields present; do not create a new message.
5. Accept the GoOmni webhook identity headers and verify the signature over the
   **raw body bytes**. Already stored delivery bodies are not rewritten, including
   older bodies without `provider` or `instance_id`; retry and replay retain those
   bytes and event IDs. Resolve them using your trusted existing channel binding.
   Explicit replay selects current destinations but does not reserialize history.
6. Use `APP_WEBHOOK`, `APP_WEBHOOK_SECRET` and
   `APP_WEBHOOK_DEVICE_MERGE_GLOBAL` for global destinations. Replace any old
   `BALE_WEBHOOK*` settings explicitly. Account-specific destinations are retained.
7. Use `none` for a new one-time schedule. Previously accepted one-time schedules
   retain their data/idempotency binding and can still execute. Sends and schedules
   continue to share the connection's idempotency namespace.

If upgrading from before GoBale 2.2, also adopt `push_name`, media `caption`,
ordinary multipart fields with endpoint-named file parts, flat
`results.message_id`, and display-ready message/edit projections in `payload`.
The webhook guide explains receipt timestamp semantics and historical bodies.

## Before switching

- Pause new requests from the consuming application and stop the old gateway.
  Never run two owners against the same database or media tree.
- Make the existing manual cold snapshot: copy the complete storage directory
  or Docker volume, `.env`, master key and service configuration. Include media
  and any SQLite WAL/SHM files left after stopping. Keep the old binary/image digest.
- Save the original database/media paths and actual Docker volume name. Do not
  run `init`, generate a new key or delete ownership locks during an upgrade.
- Review [provider limits and evidence](providers/acceptance.md). Controlled
  two-account tests do not establish exhaustive history, multi-day recovery or
  300-account live capacity.

## Binary installations

Extract the appropriate release archive into a separate code directory. Update
the service executable to `goomni` and retain the original working directory,
configuration, file permissions and storage paths. Set `APP_DATABASE` explicitly
to the original file and `APP_MEDIA_ROOT` to its original tree. Keep the existing
`APP_MASTER_KEY_FILE` or equivalent key value. Start `goomni rest` as the same
service user. No `init` or fresh login is normally required for restored sessions.

## Docker installations

Before removing the old stopped container, identify its actual volume:

```sh
docker inspect OLD_CONTAINER --format '{{range .Mounts}}{{println .Name .Source "->" .Destination}}{{end}}'
```

Use the GoOmni release's Compose file and the existing private credential/key.
Set these deployment values to your actual installation; database/media paths
are **inside the container**, not host paths:

```sh
export APP_IMAGE='docker.io/mimalef70/goomni:v2.3.0'
export APP_DATA_VOLUME='ACTUAL_EXISTING_VOLUME_NAME'
export APP_DATABASE='/app/storages/gobale.db'
export APP_MEDIA_ROOT='/app/storages/media'
export APP_MASTER_KEY="$(cat master.key)"
docker compose pull
docker compose up -d --no-build
docker compose logs --tail=100 goomni
```

Compose reads `APP_BASIC_AUTH` from the trusted local `.env`. If the old deployment
used a bind mount instead of a named volume, preserve that same mount in the
Compose file. `APP_DATA_VOLUME` has no effect on a bind mount. Never use
`docker compose down -v` for an upgrade. Recreating a container is required when
changing its environment; `docker restart` does not apply new settings.

Enable new messengers explicitly with `EITAA_ENABLED=true` and/or
`RUBIKA_ENABLED=true`, then recreate the container. Existing Bale account IDs and
credentials do not become Eitaa or Rubika accounts.

## Verify and resume

Check authenticated `/ready`, `/app/providers`, existing device instance IDs,
authentication/recovery status, pending operations and signed delivery history.
Test a new message and attachment with an operator-controlled recipient. Confirm
the consumer stores the event and validates the current headers/signature before
resuming normal work. `unknown` operations must stay unknown unless reviewed
provider proof resolves them; do not resend them to make a queue look healthy.

Transport connection, authentication and recovery are separate states. A connected
account can retain a historical recovery warning. Do not erase it or claim a
synchronized inbox merely because new messages arrive.

For rollback, stop GoOmni, restore the **complete pre-upgrade snapshot** and its
matching master key/configuration, then start exactly one owner using the old
binary/image. An older binary must not open the schema-8 database. Retain the
post-upgrade data separately if new work was accepted; restoring an older snapshot
alone cannot include that later work.
