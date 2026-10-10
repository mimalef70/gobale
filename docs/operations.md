# Operating GoOmni

This guide describes GoOmni 2.3.0. Existing data remains owned by one process;
stop the old service and copy the complete storage and master key before
upgrading. The [GoBale upgrade guide](upgrade-goomni.md) covers paths, volumes,
consumer contracts and rollback without losing accepted work.

For installation, use the [README](../README.md#how-to-use). Endpoint contracts are
in [OpenAPI](openapi.yaml); delivery behavior is in
[Webhook payloads](webhook-payload.md).

## Configuration reference

Precedence is **CLI flags → environment variables → `.env` in the working
directory → defaults**. `goomni init` generates `.env` and `master.key` with private
permissions and refuses to overwrite existing files. Relative database, media and
key paths resolve from the process's working directory. Keep credentials out of
command-line arguments, logs and public issues.

| Variable | Flag | Default | Purpose / bounds |
| --- | --- | --- | --- |
| `APP_HOST` | `--host` | `127.0.0.1` | Native listener address |
| `APP_PORT` | `--port` | `3000` | Port, 1–65535 |
| `APP_BASE_PATH` | `--base-path` | empty | Prefix every route, including health and metrics |
| `APP_UI_ENABLED` | `--ui-enabled` | `true` | Serve the embedded administrative panel |
| `APP_UI_PUBLIC_ORIGIN` | `--ui-public-origin` | empty | Exact external HTTPS origin, e.g. `https://gateway.example.com`; no trailing slash or path |
| `APP_BASIC_AUTH` | `--basic-auth` | empty; required | One administrative `username:password`, both parts nonempty |
| `APP_DATABASE` | `--database` | `storages/goomni.db` | SQLite path |
| `APP_MEDIA_ROOT` | `--media-root` | `storages/media` | Private media directory |
| `APP_MASTER_KEY_FILE` | `--master-key-file` | empty | File with a base64-encoded 32-byte encryption key |
| `APP_MASTER_KEY` | `--master-key` | empty; required without key file | Base64-encoded 32-byte encryption key |
| `BALE_APP_ID` | `--bale-app-id` | `0` | Verified client application ID; login requires 1–2147483647 |
| `BALE_API_KEY` | `--bale-api-key` | empty | Matching client application key; required for login |
| `BALE_API_VERSION` | `--bale-api-version` | `173855` | Reviewed web-client API version |
| `BALE_GRPC_ENDPOINT` | `--grpc-endpoint` | `https://next-ws.bale.ai` | gRPC-Web endpoint |
| `BALE_WS_ENDPOINT` | `--ws-endpoint` | `wss://next-ws.bale.ai/ws/` | WebSocket endpoint |
| `EITAA_ENABLED` | `--eitaa-enabled` | `false` | Opt in to the native Eitaa adapter; live acceptance pending |
| `EITAA_ENDPOINT` | `--eitaa-endpoint` | `https://hasan.eitaa.ir/eitaa/` | Authenticated HTTP TL endpoint |
| `EITAA_UPLOAD_ENDPOINT` | `--eitaa-upload-endpoint` | `https://alzheimer.eitaa.com/eitaa/` | Fixed endpoint for the complete upload transaction |
| `EITAA_DOWNLOAD_ENDPOINT` | `--eitaa-download-endpoint` | `https://mohsen.eitaa.com/eitaa/` | Authenticated media download endpoint |
| `EITAA_API_ID` | `--eitaa-api-id` | `2496` | Reviewed public web application ID |
| `EITAA_API_HASH` | `--eitaa-api-hash` | Reviewed public web application hash | Matching public client identity; not an account session |
| `EITAA_POLL_INTERVAL` | `--eitaa-poll-interval` | `5s` | Account/channel difference polling, 1s–5m |
| `RUBIKA_ENABLED` | `--rubika-enabled` | `false` | Opt in to the native Rubika adapter; live acceptance pending |
| `RUBIKA_DISCOVERY_ENDPOINT` | `--rubika-discovery-endpoint` | `https://getdcmess.iranlms.ir/` | Data-center discovery endpoint |
| `RUBIKA_API_ENDPOINT` | `--rubika-api-endpoint` | empty | Optional override of the discovered API endpoint |
| `RUBIKA_SOCKET_ENDPOINT` | `--rubika-socket-endpoint` | empty | Optional override of the discovered update WebSocket endpoint |
| `APP_WEBHOOK` | `--webhook` | empty | Comma-separated global fallback HTTP(S) URLs |
| `APP_WEBHOOK_SECRET` | `--webhook-secret` | empty | Required HMAC secret when global URLs are set |
| `APP_WEBHOOK_DEVICE_MERGE_GLOBAL` | `--webhook-device-merge-global` | `false` | Deliver to globals alongside a device override |
| `APP_SEND_WORKERS` | `--send-workers` | `4` | 1–64 globally; one active send per connection |
| `APP_WEBHOOK_WORKERS` | `--webhook-workers` | `8` | 1–64 delivery workers |
| `APP_RECONNECT_WORKERS` | `--reconnect-workers` | `4` | 1–4 concurrent reconnects |
| `APP_MEDIA_WORKERS` | `--media-workers` | `4` | 1–64 shared media transfer slots |
| `APP_QUEUE_LIMIT` | `--queue-limit` | `1000` | 1–100000 queued, sending and unknown operations globally |
| `APP_CONNECTION_QUEUE_LIMIT` | `--connection-queue-limit` | `100` | 1–100000 queued, sending and unknown operations per immutable connection; enforced alongside the global limit |
| `APP_MAX_MEDIA_BYTES` | `--max-media-bytes` | `67108864` (64 MiB) | Per-file bytes, 1–1073741824 |

`init` sets `APP_MASTER_KEY_FILE=master.key`. A configured key file takes
precedence over `APP_MASTER_KEY`, even when the latter comes from a higher-priority
configuration source. The file must have private permissions (`chmod 600`). Keep
this key separately backed up: a newly generated key cannot recover existing
sessions. Sessions and stored secrets are encrypted; message bodies and ordinary
media are not application-encrypted. Protect the data directory and backups.

`goomni init --bale-web-client` explicitly opts in to the public application
identity shipped by the reviewed Bale web client. It does not copy a browser
session or authenticate an account. Omit the flag to configure another verified
identity locally. Changing API-version or endpoint values does not establish
protocol compatibility.

`HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` apply to provider/webhook transports;
they have no GoOmni CLI flags. Media fetched from a caller-supplied URL uses direct,
destination-checked requests. Worker values are configurable bounds, not measured
messenger account capacity.

The table lists native defaults. The Docker image listens on `0.0.0.0:3000` and
uses `/app/storages/goomni.db` and `/app/storages/media`. Compose keeps that internal
port fixed; `APP_PORT` selects the published localhost port. It passes the master
key value and fixes the container storage paths, with `APP_MASTER_KEY_FILE` empty.
`APP_DATABASE` and `APP_MEDIA_ROOT` can override the defaults and must name paths
inside the container. `APP_MASTER_KEY_FILE` is not forwarded. `APP_IMAGE` selects
the Compose image, not a runtime gateway setting. Its default is
`docker.io/mimalef70/goomni:v2.3.0`; `APP_DATA_VOLUME` selects the actual named
volume, defaulting to `goomni-data` for a new installation. Set it to the existing
volume when upgrading. For a local image use `APP_IMAGE=goomni:dev` with
`docker compose build`.

## Provider availability

`GET /app/providers` reports enabled adapters for the exact running build. This
source build enables Bale by default. Eitaa and Rubika have native adapters,
disabled by default with `EITAA_ENABLED=false` and `RUBIKA_ENABLED=false`. Their
synthetic fixtures are not live acceptance. A disabled provider cannot provision
a new connection; it never falls back to Bale. Each adapter
has independent evidence and capabilities; `GET /app/capabilities?provider=bale`
requires an explicit provider. New connections require an immutable `provider`.

The descriptor's `send` object covers ordinary text/media routes and schedules,
separately from named extension operations. `kinds` lists supported request kinds;
`max_text_bytes` is a UTF-8 byte limit and `max_text_characters` counts Unicode
code points. The same limit applies to captions. Bale advertises 65,536 bytes,
Eitaa 4,096 characters and Rubika 4,200 characters. Only Bale currently advertises
`mentions_supported: true` and `max_mentions: 100`; the other adapters advertise
false and zero. All three advertise `reply_supported: true`. `media_format_notes`
describes inspected formats, not live acceptance or recipient playback. Disabled
adapters still expose implemented admission rules; an unregistered adapter has
an empty `kinds` list. Always check `enabled` and `verification` as well.

Treat returned peer IDs as opaque strings. Eitaa preserves two different group
namespaces: `{"type":"group","id":"91"}` is a classic group, while
`{"type":"group","id":"channel_91"}` is a channel-backed supergroup. A broadcast
channel remains `{"type":"channel","id":"91"}`. Keep the `channel_` prefix when
sending, querying history/media or configuring filters; do not derive a peer
from the numeric portion alone. Eitaa user and sender IDs remain positive
decimal strings. Existing Bale and Rubika ID formats are unchanged.

Keep global routing in `APP_WEBHOOK`, `APP_WEBHOOK_SECRET` and
`APP_WEBHOOK_DEVICE_MERGE_GLOBAL`; replace old BALE_WEBHOOK settings explicitly.
Bale protocol credentials/endpoints retain their BALE_ prefix.

Native protocols and authentication steps remain separate. Eitaa uses bounded
HTTP TL calls and account/channel difference polling; Rubika uses encrypted HTTP
calls and an update socket. The client decides whether OTP or password comes
first. Follow `state`, `delivery` and `available_deliveries`; do not infer numeric
wire codes or force the Bale login order. The CLI follows these states under the
same immutable account guard.

Eitaa image sends inspect JPEG/PNG bytes. Voice and audio currently require
complete single-stream Ogg Opus; other codecs can be sent as files. Video sends
inspect a nonfragmented MP4 with one AVC video track, bounded dimensions and
consistent container timing. This inspection does not decode video frames or
prove recipient playback. Albums accept 2–10 image/video items. Every compound
upload/send phase is journaled before provider contact; a later uncertain result
stays `unknown`, retaining its media and stage history without automatic resend.
User avatar downloads resolve the current authenticated reference and fully
inspect bounded JPEG/PNG/GIF bytes before returning a stream. Rubika avatar
mutations derive the provider's 200×200 and 800×800 square JPEG renditions from
the inspected source; each upload is journaled before the profile change. This
specific avatar preparation is not a general media transcoder. Eitaa avatar
history is a bounded paginated read; deleting an older photo requires its fresh
reference among the first 100 own photos, otherwise the operation fails safely.

The reviewed Eitaa web worker explicitly intercepts sticker/GIF, reaction and
several draft methods with local stubs (`eitaaNoSend`). Their TL declarations and
inherited client wrappers do not establish server support. These methods are
excluded from native admission; the adapter does not return empty local success
or guess a server implementation. Check the provider inventory for exact gaps.

Native read results remain provider-specific. Eitaa dialogs return actual dialog
rows, not every cached user or group. Its history/dialog `next_cursor` is signed
and bound to the account, immutable connection, operation and peer; do not modify
or reuse it on another connection. Session-token renewal can invalidate an older
cursor, requiring a fresh first page. `has_more` is a bounded continuation hint;
`complete=false` does not claim an exhaustive export. Non-advancing boundaries
stop with an explicit incomplete result. History only advertises downloadable
media after the private account-scoped reference has been persisted.

Eitaa history uses one bounded native page of 50 rows even when the caller
requests fewer; controlled reads through both native and official Web RPC returned
a stale window for `limit=5`. A request for 100 returned 50 rows with a placeholder
count of 1000; neither that count nor the larger requested limit proves the end.
The gateway returns at most the requested number and
builds `next_cursor` from that output, so omitted rows remain available on the next
page. A zero-ID channel migration notice is not a message: history counts it as
`omitted_service_notices`, while durable update recovery retains `chat.migrated`.
Rubika history sends the requested limit and returns `next_offset_id` based on
the smallest delivered message ID minus one, matching its inclusive `max_id`
semantics. It does not use a server boundary beyond locally omitted messages.
Neither provider's `complete: false` becomes a promise of exhaustive export.

Eitaa and Rubika retain old mixed recovery warnings as
`recovery_issue: legacy_unclassified`. This is uncertainty, not proof of missing
messages, and is not silently erased. Eitaa `updateChannelTooLong` requests its
independent channel difference stream; only an actual `differenceTooLong` or
`channelDifferenceTooLong` records the corresponding expired-state reason.
An update without enough metadata to bind deleted/read message IDs to a peer has
the separate `unresolved_message_peer` reason. Unsupported variants remain
durable diagnostics and coverage warnings, without inventing a lost inbox.
Initial attachment still starts from a provider baseline. Rubika accepts the
bounded interval messages with that baseline; it does not silently skip the first
messages of a newly discovered conversation or claim to import all old history.
After Rubika expires a mutation state, rebaselining preserves an existing
new-message watermark. Continued `FromMin` pages can recover newer messages across
restarts; the warning about unproven historical edits/deletions remains visible.

Eitaa static locations use the reviewed current Android input layout and require
the persisted send nonce to match the provider acknowledgement. Earlier mismatched
acknowledgements stay unknown. Video uploads include streaming metadata and, when
the bounded AVC decoder supports the first frame, a real JPEG preview. The preview
upload identity is journaled before any provider write. Tested two- and five-second
clips retained video attributes; one-second clips returned as files, including in
the official Web client. Do not change duration or add audio to disguise that
provider behavior; inspect the received media type. No runtime transcoder is used.

Media and reconnect admission retain global worker limits and reserve a quota
for each configured active provider. Each quota is at least one and otherwise
`floor(global capacity / active provider count)`; a single provider uses the full
capacity. Scheduling selects providers and then connections in rotation, while
preserving FIFO within a connection. A blocked provider cannot consume every
slot. Pending media admission is bounded to 1000 globally and 100 per connection;
`RESOURCE_BUSY` rejects excess admission. HTTP media admission waits at most 45
seconds. A handed-off stream keeps its own lifetime and frees its slot on close
or cancellation. These bounds are isolation mechanisms, not capacity evidence.

See the [provider source inventories](providers/sources.md) for current evidence
and remaining native/live gates. Existing Bale verification remains independent
of Eitaa/Rubika implementation and does not establish their interoperability.

## Deployment and access

Run **one process per database and media directory**. SQLite uses WAL, FULL
synchronization and an OS ownership lock. Do not remove a running process's lock,
share its SQLite file over network storage or attach active replicas to one volume.

Administrative Basic Auth grants access to every connected account. The consuming
application must authorize its own users and organizations. Keep the listener
private or use TLS at the ingress. An explicit invalid device selector fails;
reusing a deleted alias does not transfer its old connection's data or work.

All account-scoped machine and browser requests require an explicit selector and
`X-Device-Instance` matching the saved connection. The consuming backend derives
these from an authorized channel record; customer browsers never receive the
administrative Basic credentials. Missing selectors/instances fail with 400;
stale instances fail with 409 before provider work. See the
[consumer integration contract](consumer-integration.md).

`APP_CONNECTION_QUEUE_LIMIT` defaults to 100 outstanding operations per immutable
connection, alongside the global `APP_QUEUE_LIMIT` of 1000. Both include queued,
sending and unknown work. Full admission returns 429 `CONNECTION_QUEUE_FULL` or
`QUEUE_FULL`; no new operation is accepted. Identical idempotent retries can still
read existing work. Scheduled occurrences blocked by admission remain due and
unconsumed. These bound outstanding work. Consumers still enforce their own
connection, request and upload budgets and manage retained storage separately.

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

### Docker without Compose

Run these commands from a dedicated configuration directory in a POSIX shell on
Linux or macOS. The following commands use the pinned release image and a named
volume. For a local source build, use `goomni:dev` instead. Skip initialization if the directory already
has `.env` and `master.key`.

```sh
mkdir -p goomni-data
cd goomni-data
export APP_IMAGE=docker.io/mimalef70/goomni:v2.3.0
docker pull "$APP_IMAGE"
docker run --rm --user "$(id -u):$(id -g)" \
  --volume "$PWD:/config" --workdir /config \
  "$APP_IMAGE" init --bale-web-client

export APP_MASTER_KEY="$(cat master.key)"
docker volume create goomni-data
docker run --detach --name goomni --restart unless-stopped \
  --publish 127.0.0.1:3000:3000 --env-file .env \
  --env APP_HOST=0.0.0.0 --env APP_PORT=3000 \
  --env APP_MASTER_KEY --env APP_MASTER_KEY_FILE= \
  --env APP_DATABASE=/app/storages/goomni.db \
  --env APP_MEDIA_ROOT=/app/storages/media \
  --volume goomni-data:/app/storages \
  --read-only --tmpfs /tmp:size=67108864,mode=1777 \
  --security-opt no-new-privileges:true --cap-drop ALL \
  --log-driver json-file --log-opt max-size=10m --log-opt max-file=5 \
  --stop-timeout 30 --add-host host.docker.internal:host-gateway \
  "$APP_IMAGE"
```

Open `http://localhost:3000/ui/` (include `APP_BASE_PATH` if set) and use the
administrative credentials generated in `.env` to connect a messenger account. The
standalone command fixes the host port at 3000; change only the first port number
in `--publish` to choose another. Unlike Compose, `--env-file` does not interpolate
shell expressions or automatically forward host proxy variables. Put any required
proxy values in that private file or pass them explicitly with `--env`.

The standalone and Compose examples both use `goomni-data` by default. Choose
different names for independent installations; never start both against the same
volume. Replacing an image does not import a native process's host storage.
Do not run native, standalone Docker and Compose services against the same data
or published port. Use the [backup and upgrade procedure](#backup-restore-and-upgrades)
before reusing an existing volume. To apply changed environment values, stop and
recreate only the service container while preserving its volume and master key.

## Administrative panel

`APP_UI_ENABLED=true` (default) serves the embedded panel at `/ui/`, including
`APP_BASE_PATH` when configured. The same artifact contains the UI and API; assets
are never downloaded at runtime. Missing, stale or corrupt assets fail startup
before database ownership or account startup. Rebuild the complete artifact, or
explicitly disable the UI for a Go-only/API-only deployment. UI metadata requires
no database migration; upgrading to a newer source revision may still migrate
storage.

Local browser access accepts only `localhost`/loopback Host values. For remote
access set `APP_UI_PUBLIC_ORIGIN=https://gateway.example.com` (no trailing slash
or path), terminate HTTPS at a trusted reverse proxy, and preserve that public
Host header when forwarding to the private GoOmni listener. With a base path such
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
invalidates every panel session but preserves encrypted messenger sessions. Login has
per-IP and global limits; proxies can share the per-IP budget. Keep credentials
out of URLs and proxy logs. Only language/theme are saved in localStorage.

`/ui/api/` exposes the finite management subset under browser-session auth; cookies
do not authorize the public Basic API, and Basic alone does not authorize UI API.
All account-scoped UI requests require `X-Device-Instance`. A stale tab gets
`409 DEVICE_INSTANCE_CHANGED` if its alias now belongs to another connection.
External Basic clients must also send `X-Device-Instance` on every account-scoped
request; update existing consumers before using the current API contract.

Snapshots read local status and batch queue counts; polling does not call the provider.
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
| `UI_UNAUTHORIZED` after a restart or idle period | Sign in to the panel again. This alone does not require logging the messenger account in again. |
| A pending messenger login returns to the phone step | A challenge expires or is lost on restart; request a fresh code explicitly. A page refresh can only resume a still-valid challenge in the same server process. |
| `DEVICE_INSTANCE_CHANGED` | Refresh the account list and reselect the intended connection; the old alias was replaced. Do not retry the stale action against the replacement. |
| `UI_LOGIN_RATE_LIMITED` or `AUTH_RESEND_TOO_SOON` | Wait for the indicated retry time. Refreshing the page or opening a second tab does not reset the server-side limit. |

Panel sign-out, messenger logout and connection deletion are different actions. The
latter two cancel unsent account work; neither promises to erase retained history,
media, ambiguous sends or audit records. See [Retention and disk use](#retention-and-disk-use).

## Backup, restore and upgrades

Keep a protected backup of the encryption key **separate from** database backups.
Generating a new key does not recover sessions encrypted by the previous key.

For a consistent cold backup:

1. Stop GoOmni cleanly and confirm its process/container has exited.
2. Copy the complete storage directory or named volume, including the media tree.
3. Preserve the matching key and record the configuration, binary version and
   source revision or immutable image digest.
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

New installations default to `goomni.db` and the `goomni-data` volume. Upgrades
must explicitly retain their original database path (often `gobale.db`), media
root and actual named volume through `APP_DATA_VOLUME`. Otherwise Docker can
silently create an empty installation. Internal media lock/staging names and
cryptographic storage labels retain their original values; never rewrite or
remove them to match the product name. See the [upgrade guide](upgrade-goomni.md).

Migrations run at startup; take a backup before upgrading. Foreign databases and
wrong master keys are rejected. Roll back with a compatible database snapshot,
not an arbitrary older binary against a newer schema. Check release notes for
protocol and storage changes before replacing the running version.

Historical GoBale upgrades reached schema 7. Schema 8 assigns
Bale to every existing connection, including retired connections, and preserves
sessions, IDs, queued/unknown work, schedules and private media references. The storage schema number is independent of the application
version. Stop the old process, back up its complete storage and key, deploy GoOmni
with the same key, and update consumers before resuming requests. Do not run a
older binary against the migrated database; rollback requires the pre-upgrade
snapshot and the matching old binary/configuration.

The 2.2 HTTP/webhook upgrade also requires consumers to use `push_name`, media
`caption`, the new ordinary multipart fields and flat `results.message_id`.
New message/edit events put the display-ready message in `payload` and native
content in `content`; receipt timestamps use `read_date`/`received_date`.
Previously accepted work is retained through the provider migration. Already stored
signed webhooks keep their original format even when delivered or replayed after
upgrading. Coordinate consumer handling before restarting delivery; see the
[consumer contract](consumer-integration.md) and [webhook payloads](webhook-payload.md).

Consumers upgrading from 1.x must also adopt the 2.0 HTTP changes:

- Select every account explicitly and retain its `instance_id`; send
  `X-Device-Instance` on account-scoped reads and writes. Never automatically
  rebind a stale alias after a 409 response.
- Persist stable `Idempotency-Key` values for device creation, immediate sends and
  schedule creation. Device provisioning has its own global key namespace;
  sends and schedules share a namespace within each immutable connection.
- Bind newly stored webhook events using the signed `instance_id`, `session_id`
  and provider `device_id`. Previously stored bodies remain unchanged; choose an
  explicit handling policy for old events that lack the instance field.

Use the version, source revision and immutable image digest to identify the
build actually deployed. v1.0.0 does not provide this newer HTTP contract.

Schema 7 adds the device provisioning journal. Connection, initial encrypted
webhook configuration and provisioning key commit together. Replays retain the
original immutable connection, including after restart; retired keys cannot attach
to a reused alias. Existing sessions, events, deliveries and checkpoints remain.
The current HTTP contract requires provisioning keys and immutable instance
headers; update API consumers with the binary. Stored webhook bodies from before
the instance field was introduced remain byte-identical on retry and replay.

Schema 6 adds durable outbox positions, authoritative schedule occurrences, event
query indexes and device webhook filters. Migration preserves existing queue order.
Old executions are not inferred from client-controlled idempotency keys; schedules
with earlier occurrences expose `occurrence_history_complete: false`. New
occurrences, operations and schedule advancement commit together.

Schema 5 introduced durable webhook queue positions. Existing rows retain their
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

Registered uploads, events, delivery history, operations, schedules and idempotency
records have no automatic TTL. Disk consumption is not bounded by the queue limits.
A single bounded cleaner removes only proven application-owned temporary uploads;
it does not implement historical retention or selective erasure.

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
history. A disk-full persistence failure needs operator attention; GoOmni must
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

Current protocol and operational limitations and verified scope are summarized in the
[README](../README.md). Worker limits, synthetic benchmarks and two-account tests
must not be interpreted as proven live deployment capacity.

### Metrics and request limits

`/metrics` reads cached queue/disk/media snapshots. The background sampler runs
once every five seconds; media traversal is incremental and bounded. Inspect
`goomni_metrics_snapshot_timestamp_seconds` and
`goomni_metrics_snapshot_stale` before using cached values. Last successful
samples remain available during storage failures; `/ready` returns 503.
Metrics include SQLite pool waits, transaction/commit/persistence latency,
reviewed SQLite error categories, busy workers, reconnect attempts, queue ages,
database/WAL/media bytes and available storage. `goomni_storage_free_bytes`
measures the database filesystem; `goomni_media_free_bytes` measures the media
filesystem independently, with snapshot component `media_disk`. Labels contain no account,
message, credentials or destination identifiers.

Synchronous JSON operations have a 45-second context deadline. Immediate sends
wait up to 40 seconds and then return their durable operation; disconnecting an
HTTP client does not cancel accepted work. Streaming media retains its own
transfer lifetime. Recovered handler panics return a fixed 500 response without
panic text. Docker Compose rotates five log files of 10 MiB each.

### Temporary uploads

The gateway exclusively locks its media root as well as its database. Upload
intents live under `.gobale-staging`; an active upload cannot be swept. Successful
registration retains the immutable final file and removes its temporary intent.
Crash recovery checks all registrations, including deleted connections. Database
errors, ambiguous commit outcomes, unexpected paths and ownership conflicts keep
the files for diagnosis. A minute-based worker retries released intents.

Legacy `.upload-*` files are considered only during startup and only after
ownership and registration checks. Unregistered final files without ownership
proof are reported rather than deleted. Never scan global temporary directories
or manually remove files while GoOmni owns the data. Schedules share registered
uploads: completing or cancelling one schedule does not delete that media.

### Operation and event queries

`GET /send/operations` filters durable work by `state`, `kind`, `operation`,
`peer`, `schedule_id`, `created_after` and `created_before`. `GET /send/schedules`
supports the same applicable filters using `state`. Inspect
`GET /send/schedules/{id}/occurrences` for each materialized execution and its
current operation result. Schedule completion means no future executions remain,
not that every provider send succeeded.

`GET /events` and `GET /chat/{peer}/messages` search locally stored events using
`search`, `event`, `direction`, `sender_id`, `start_time`, `end_time` and
`media_only`; `/events` also takes `peer=type:id`. Search is a case-sensitive
literal Unicode substring of reviewed text/caption fields, at most 512 UTF-8
bytes. Percent and underscore are literal characters. Edits/deletions remain
separate events; this is neither reconstructed chat history nor a provider import.
Queries are scoped to the selected immutable connection. Lower time bounds are
inclusive, upper bounds exclusive; pagination defaults to 50 with a maximum 100.

Bale text/media sends and schedules accept `mentions: ["123", "456"]`:
up to 100 unique canonical positive uint32 strings and nonempty message/caption.
Mentions are included in idempotency comparisons and remain live-unverified.
Eitaa and Rubika reject nonempty mentions; consult the selected provider's
`send` capabilities before offering them.

Scheduled `message.forward` uses the `operation`/`payload` schedule shape with
the selected provider's contract. All three adapters require `peer`, `source_peer`
and string `message_id`; Bale also requires positive-string `source_date` and
accepts optional `hide_sender`. Eitaa accepts optional `hide_sender` but no
`source_date`; Rubika accepts neither field. Message and peer ID formats remain
provider-specific. Every occurrence gets its own persisted request ID. The source
reference is retained, not a copy of its content. A missing source at execution
produces the normal failed/unknown outcome; unknown work is not blindly resent.
Controlled one-time scheduled forwards were acknowledged for all three
providers; other recurrence and forwarding variants remain live-unverified.
Other provider operations can be
scheduled only when their capability entry explicitly sets `schedulable: true`.

### Reproducible capacity acceptance

The mixed test uses real REST handlers, SQLite and signed HTTP webhooks with a
synthetic provider. Use `--providers bale,eitaa,rubika` for 100 accounts of each
provider in a 300-account run; this requires equal allocation. Separate native
fixtures exercise actual Go protocol clients against local HTTP/socket servers.
Neither fixture establishes a live provider account quota.
The acceptance target is 300 connections, Linux with 4 CPU / 8 GiB, 60 incoming
events/s and 10 new sends/s (8 immediate, 2 scheduled), a threefold 60-second
burst, ten read/search requests/s and one 1 MiB upload every ten seconds.
Production worker counts and the 500 ms poll interval are retained.

```sh
# Build the separate local runtime image first; host Go 1.26.9 compiles the tests.
docker build --file docker/golang.Dockerfile --tag goomni:dev .
# Uses only synthetic identities and isolated Docker volumes, no messenger network.
python3 scripts/start_soak.py --providers bale,eitaa,rubika --accounts 300 --duration 10m --warmup 0s --max-disk-gib 1 --image goomni:dev --wait
# Bale native client fixture in the same Linux 4 CPU / 8 GiB environment.
python3 scripts/start_soak.py --native --duration 1s --warmup 0s --accounts 300 --wait
# Existing Bale sequence: native fixture, service smoke, 50/150/300, 1h, then 24h.
python3 scripts/run_capacity.py
# Collect a detached run's final verdict; running never means passed.
python3 scripts/start_soak.py --collect artifacts/soak/RUN/run.json
```

The Eitaa and Rubika native fixtures each exercise 100 independent clients
against local protocol servers, including authentication, updates and reconnect.
Run them explicitly from `src`; this is synthetic protocol evidence, separate
from the mixed service/storage run and from live provider acceptance:

```sh
GOOMNI_MIXED_CAPACITY=1 go test -race ./internal/eitaameow ./internal/rubikameow -run '^TestRunConcurrent100NativeClients$' -count=1 -v
```

The image is an isolated runtime base; the runner compiles and mounts a separate
Linux test binary using the same pure-Go SQLite variant shipped in containers.
It records commit, source fingerprint, binary checksum, image ID, resource bounds,
seed, rates, global/per-connection queue limits and final verdict. All mixed
stages reuse one frozen binary; the native fixture has its own binary. Source
changes invalidate reuse.

Docker must provide at least 4 CPU and 8 GiB. `--max-disk-gib` defaults to 16 for
the standalone runner and the sequence's short/one-hour stages. Preflight requires
that budget plus 2 GiB free on both the host and the Docker volume filesystem;
set a smaller explicit budget only when appropriate for the test duration. The
24-hour stage derives its budget from a passed one-hour disk-growth measurement,
with 50% growth headroom plus 2 GiB, and still requires the free-space reserve.
Insufficient preflight space records `blocked_insufficient_space`; it is neither
a completed capacity test nor a capacity failure. The tools do not delete retained
work to make room. `run_capacity.py --resume PATH/acceptance.json` resumes a
disk-blocked sequence only with matching frozen source, binary and image evidence.

Reports distinguish
scheduled load, actual offered/admitted/rejected work, generator misses and
completed work. Output stays in ignored `artifacts/soak/`. A launch or an
unfinished run is not acceptance evidence. Actual live checks require separately
identified operator-controlled accounts and recipients.
