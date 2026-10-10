# Changelog

## 2.3.0 — 2026-10-10

**GoBale is now GoOmni.** One native Go binary serves independent Bale, Eitaa
and Rubika accounts through a shared REST API, administrative panel and signed
webhooks. The repository, module, CLI and images now use `goomni`. Existing
release tags remain historical GoBale builds. No major version is increased.

**Coordinated consumer upgrade required.** Follow the
[upgrade guide](https://github.com/mimalef70/goomni/blob/v2.3.0/docs/upgrade-goomni.md)
before replacing an existing deployment. Reuse the original database, media,
volume and master key; do not run `init` or create an empty new volume.

- Add immutable `provider` selection, provider registries, operation validation,
  capability discovery, isolated encrypted sessions/media references and atomic
  event/checkpoint batches. Existing and deleted accounts migrate to Bale in
  storage schema 8. Connection IDs, guards, provisioning bindings, queued/unknown
  operations, schedules and already signed webhook bytes are preserved.
- Add native Go Eitaa and Rubika clients with phone/code/password login, session
  restore, bounded receive/recovery, messaging/media, account and group/channel
  operations. They are opt-in (`EITAA_ENABLED`, `RUBIKA_ENABLED`); capability
  catalogues and dated evidence state each operation's actual coverage.
- Share ordinary sends, uploads, replies, forwards, scheduling and operation
  tracking across providers. Expose text/caption limits, optional interactions,
  receipt semantics and avatar support. Normalize projected chat IDs and preserve
  explicit sparse edits without inventing missing text, sender or timestamps.
- Add provider selection/filtering and provider-aware authentication steps to
  the English/Persian administrative panel. Keep its session, CSRF and immutable
  instance protections. There is no consumer user model or chat console.
- Journal compound upload/album/avatar stages before native writes; uncertain
  results remain observable and are never blindly retried. Fair bounded workers
  reserve progress across providers and connections. Logout/deletion preserve
  accepted audit history and ambiguous work.
- Correct Eitaa authorization/token renewal, peer namespaces, location encoding,
  image/avatar preparation, real video previews and history continuation beyond
  the observed 50-row server cap. Correct Rubika discovery/registration, contact
  cards, sparse edits, member identities and independent message watermarks.
  Separate unsupported update types from proven gaps and retained unclassified
  historical warnings; a socket connection does not mean recovery is complete.
- Rename current webhook identity headers to `X-GoOmni-Event-Id` and
  `X-GoOmni-Delivery-Id`, metrics to `goomni_`, and gateway-wide webhook settings
  to `APP_WEBHOOK*`. HMAC signing and stored delivery bytes retain their meaning.
  New provisioning requests require a provider. New one-time schedules use `none`;
  already accepted schedules retain their original bindings and behavior.
- Ship the embedded UI, current OpenAPI, upgrade/integration/operations guides,
  provider evidence and license notices in release archives. Publish matching
  Linux amd64/arm64 images to Docker Hub and GHCR, with immutable version tags,
  provenance/SBOM and anonymous asset/registry/restart verification. New installs
  use `goomni.db` and a named `goomni-data` volume; upgrades select existing paths.

### Verified scope and remaining limits

- Dated controlled trials used two operator accounts per provider. They cover
  native login/session restore, text/media, signed webhooks and selected group,
  profile, scheduling and history operations. A real one-hour process outage
  recovered all 75 Eitaa/Rubika test messages, with no missing or mis-scoped event
  copies, checkpoint rewind or resend of the 18 pre-existing unknown operations.
  These results identify their pre-release binaries separately from this release.
- Eitaa static location passed in both directions. Tested two- and five-second
  videos retained video presentation; tested one-second clips arrived as files,
  also through the official Web client. Poll writes and typing/cancel remain
  rejected by the tested server. Source-only reaction/sticker/GIF handlers are
  excluded from advertised availability. Other contact/card/codec variants
  retain their recorded limits.
- Bale ordinary-user keyboard templates remain rejected. Mentions, unrestricted
  history export, months-old archives, multi-day recovery and sustained Linux
  live-account capacity remain acceptance gates. Synthetic 300-client fixtures
  and short capacity runs do not establish 300 real accounts or a 24-hour soak.
  Provider inventories distinguish source review, offline tests and controlled
  live results. Uncertain media edits can withhold downloads instead of serving
  potentially stale bytes. No financial mutations or call engine are added.

## 2.2.0 — 2026-10-09

**Breaking public API and webhook changes.** Update send/name inputs, operation
response readers and webhook consumers with this release. Storage remains at
schema 7; existing sessions, accepted operations, schedules and signed webhook
bodies are retained. Previously stored events keep their original delivery format.
This release aligns selected shared contracts with GOWA; it is not a drop-in
WhatsApp API replacement and does not add unsupported Bale capabilities.

- Align shared HTTP fields with GOWA: `push_name` for account name changes,
  `caption` for media text, ordinary multipart fields with endpoint-named
  `file`/`image`/`video`/`audio` parts, and `ptt` for existing Ogg Opus audio.
  Unsupported options and old input aliases are rejected. No transcoder is added.
- Expose acknowledged `message_id` directly in operation results while preserving
  `send_id`, durable state and pending/unknown error semantics. Scheduled sends
  return `status`, `schedule_id`, `scheduled_at` and `next_run_at`; schedule detail
  and occurrence endpoints retain their durable administrative records.
- Accept scheduled multipart media in one request. Register the media and schedule
  atomically, bind idempotency to bytes/metadata/schedule content and retain the
  original asset on concurrent retries, restart and completed-schedule retries.
- Put newly serialized message/edit projections in webhook `payload`, with
  reviewed native content in `content`. Preserve old signed bodies, identities,
  historical decoding, local search and reconciliation evidence. Real Bale peer
  IDs, receipt semantics and unavailable author information remain explicit.
- Correct new receipt payloads: expose `read_date` and `received_date` according
  to the provider fields, and remove `date`, `range_status` and `range_valid`
  from those projections. The reviewed web client uses `startDate` as a peer
  watermark, not the lower endpoint of a range ending at the observation date.
  Own-read retains its distinct optional `end_date` without range-validity labels.
  Preserve zero timestamps, native event identities and all previously persisted
  webhook bytes on duplicate acceptance, restart and replay. No exact receipt
  message IDs or reconciliation of unknown sends is introduced; boundary behavior
  and per-message coverage remain live-unverified.
- Build and test with Go 1.26.9 and update `golang.org/x/net` to v0.60.0 for the
  security advisories reported against Go 1.26.6 and x/net v0.57.0. Pin the official
  Go container image by its verified digest and align CI and contributor setup.

### Verified scope and remaining limits

- Contract, storage, retry and parser changes have synthetic regression coverage,
  including concurrent multipart scheduling, restart, idempotency conflicts and
  byte-identical historical webhook retry/replay. No new live-account or consumer
  application acceptance is claimed. Voice still requires complete Ogg Opus;
  there is no runtime conversion. Receipts retain Bale's timestamp semantics,
  and history remains an event-oriented API rather than GOWA's message records.
- Ordinary-user keyboard-template sends remain rejected in live tests. Long-gap
  recovery, exhaustive history export, long-duration Linux capacity and deployment
  retention remain acceptance gates. Mentions, scheduled forwarding, WebP sends,
  account-security/report/story writes and Mini App credential interoperability
  retain their documented live-verification limits. No financial mutation support
  is claimed.

## 2.1.1 — 2026-10-08

- Resolve first-message sender names through the reviewed bounded recent-dialog
  scan when the account's contacts do not contain a trusted user reference.
  Keep contact refresh, dialog pagination and profile reads within one bounded
  lookup; unavailable names remain explicit.
- Wait for the next per-connection sender lookup slot before event persistence,
  so messages from two cold senders arriving close together do not lose the
  second lookup solely to throttling. Keep the ordered update queue bounded,
  cancel lookup waits on disconnect, and give each durable event acceptance its
  own storage timeout after enrichment, including during recovery.
- Keep already persisted webhook bodies, event IDs and replay content unchanged.
  Enrichment runs automatically; no name-completion event is introduced. These
  fixes do not extend live-provider, recovery-gap or capacity acceptance claims.
- Retry transient failures during anonymous release verification downloads with
  bounded backoff. Keep access denials, missing assets and validation failures
  terminal; publication is never retried by the download verifier.

### Verified scope and remaining limits

- Sender-name fixes have synthetic regression coverage; they do not establish
  new live-provider or consumer-application acceptance. Existing signed webhook
  bodies and retry/replay identity remain unchanged.
- Ordinary-user keyboard-template sends remain rejected in live tests. Long-gap
  recovery, exhaustive history export, long-duration Linux capacity and deployment
  retention remain acceptance gates. Mentions, scheduled forwarding, WebP sends,
  account-security/report/story writes and Mini App credential interoperability
  retain their documented live-verification limits. No financial mutation support
  is claimed.

## 2.1.0 — 2026-10-08

- Add a consumer-ready `message` projection to newly accepted message/edit events
  and history, with account-scoped sender names, display text, reply/edit identity,
  separate forward provenance and consistent attachment metadata. Forwarded text
  and media no longer require walking nested quote content. Polls and unsupported
  variants have explicit display fallbacks. Previously persisted webhook bytes and
  event IDs are preserved for retry/replay; unavailable names remain explicit.
- Expose structural receipt range validity without guessing the meaning of a zero
  date, inventing message IDs or treating a range as proof for an unknown send.
- Accept one-request multipart file/image/video/audio/voice sends on existing media
  send routes. Atomically register the file with a durable outbox request and RID;
  exact file/request retries share the existing connection idempotency namespace.
  `ptt=true` on multipart audio selects verified Ogg Opus voice validation, without
  conversion. Multipart scheduling is not supported; upload separately first.
- Accept bounded WebP headers for native image sends with matching MIME. Native
  provider acceptance/rendering of WebP remains live-unverified.
- Return alias, immutable instance, bound account, safe challenge metadata and
  server time with authentication, transport and recovery in one status response.
- Mark webhook HTTP 4xx failures terminal except 408/425/429, retaining the ledger
  and explicit retry/replay while allowing subsequent events to progress. Network
  errors and other unsuccessful responses retain bounded retry behavior. Consumers
  should return 503 for temporary persistence failures and 422 for permanent ones.
- These changes do not extend dated live-account, recovery, capacity or consumer
  application acceptance claims.

- Add `make release VERSION=...` to coordinate reviewed-commit CI, immutable tag
  CI, dual-registry publication and API documentation deployment. Publication now
  finishes with anonymous verification of all four archives, manifest/checksums,
  tag revision and both registry digests, plus native Linux amd64/arm64 clean-volume
  container restart tests. Existing tags/releases are never overwritten.

## 2.0.1 — 2026-10-07

GoBale 2.0 introduces a breaking machine-API contract for safe multi-account
integrations, together with durable provisioning, richer local queries and
operational improvements. Update consumers with the gateway and back up the
complete storage volume and encryption key before upgrading from 1.x.

The earlier `v2.0.0` source tag was not published as release archives or container
images. This section includes the full 2.0 change set.

### Breaking API and storage upgrade

- Require an explicit account selector and `X-Device-Instance` on every
  account-scoped machine or browser request. Missing selectors/guards fail with
  400; reused aliases with an old instance fail with 409. Do not automatically
  refresh the instance and repeat a stale write.
- Require stable `Idempotency-Key` values for `POST /devices`, immediate sends and
  schedule creation. Device provisioning keys are global to the gateway database;
  sends and schedules share a separate namespace within each immutable connection.
  Changed content under a key fails with 409.
- Atomically provision the connection, encrypted initial webhook configuration
  and key binding in schema 7. Response-loss/restart retries return the original
  connection; a retired key cannot attach to a newly reused alias.
- Add `instance_id` to newly stored event/webhook bodies. Receivers should bind
  it together with `session_id` and the provider `device_id`. Previously persisted
  bodies retain their exact bytes and retry/replay identity, including the absence
  of this field; consumers need an explicit policy for that historical backlog.
- Migrate v1.0.0 storage from schema 5 through schema 6 to schema 7. Preserve
  sessions, checkpoints, events and durable work. Schema 6 persists outbox order
  and records new schedule occurrences atomically; old unrecorded occurrence
  history is marked incomplete. The schema number is independent of the app
  version. Rollback requires the pre-upgrade database/media snapshot, matching
  key and old binary; do not open migrated storage with a 1.x binary.

### Features and reliability

- Wait for SQLite driver rollback and connection close before releasing the
  process ownership lock. A replacement process cannot open the database while
  cancellation cleanup is still running; a failed or timed-out drain retains
  ownership for a later close attempt.
- Add filtered operation, schedule and local event queries, authoritative
  schedule executions, scheduled forwarding and explicit mentions in text/media
  captions. Each schedule occurrence and its operation commit together.
- Add per-device peer/sender/direction webhook filters and English/Persian panel
  controls while preserving queued delivery targets and stable retry/replay identity.
- Bound outstanding work per connection (default 100) and globally (default 1000).
  Admission includes queued, sending and unknown operations; blocked schedule
  occurrences remain due without consuming an execution.
- Update the CLI login helper for keyed provisioning and immutable account guards.
  Retain the selected connection through response loss and alias replacement;
  retries of provisioning reuse their key while authentication is not blindly retried.
- Add cached bounded operational metrics, request deadlines, safe panic responses
  and Docker log rotation. Keep SQLite WAL/FULL durability and one process owner.
- Clean only provably owned temporary uploads after failures/restarts; retain
  registered uploads and history without TTL. Avoid redundant voice spool copies.
- Add process-crash, disk-full and read-only failure tests, real REST CLI login
  regression tests and reproducible mixed/native synthetic capacity harnesses.
- Reorganize installation, API examples, upgrade guidance and consumer integration
  documentation. Consumer applications retain ownership of their users and
  permissions; GoBale remains independent of their data models.
- Build one Linux amd64/arm64 container index for GHCR and Docker Hub, with
  provenance/SBOM metadata. Release automation checks that both registry tags
  are unused before publication and verifies their matching digest afterward.

### Verified scope and remaining limits

- This release preserves the dated native evidence from two authorized Bale
  accounts recorded on 2026-10-06; it does not establish new provider compatibility
  from route counts or offline tests. Mentions and scheduled forwarding remain
  live-unverified. Ordinary-user keyboard-template sends remain rejected in live tests.
- Long-gap recovery, exhaustive history export, long-duration Linux capacity
  and deployment retention acceptance remain gates. A 300-client synthetic
  fixture does not establish 300 real accounts or completed 24-hour stability;
  incomplete runs do not establish production capacity.
- Account-security/report/story writes and Mini App credential interoperability
  retain their documented live-verification limits. No financial mutation support,
  active-active deployment, automatic historical retention or complete backup
  facility is claimed. See the native capability inventory and AGENTS.md.

## 1.0.0 — 2026-10-06

The first 1.0 gateway release includes the embedded administrative panel, durable
account-isolation and recovery fixes, four native binary archives, and a non-root
Linux amd64/arm64 container.

- Embedded English/Persian administrative UI at `/ui/`, with account login and
  lifecycle, recovery status, per-device webhook settings and delivery management.
- Isolated cookie/CSRF browser authentication, immutable device-instance guards,
  resumable public challenge metadata and enforced OTP resend cooldowns.
- Local overview snapshots and filtered delivery lists avoid per-account provider
  polling. Existing Basic API consumers retain their contracts.
- Version/contract-checked UI assets, local fonts, third-party notices and build
  stages for the single binary and non-root Docker image. Node is build-only.
  UI changes need no database migration and do not expand Bale compatibility claims.

- Bound REST requests to immutable connection IDs, including streamed bodies and
  device-path operations, so deleting and reusing an alias cannot switch accounts.
- Persist unresolved stream-recovery gaps before accepting newer events; completed
  catch-up now exits recovering without hiding gaps on another route.
- Added schema 5 durable webhook queue ordering. Manual retries retain their
  position; replay appends deliberately. Back up before upgrading; older binaries
  require restoration of the previous database snapshot for rollback.
- Included native voice in exact account/peer/request-ID own-message reconciliation.
  Existing unknown voice operations can use already-persisted proof on startup;
  duplicate events use their original stored body, never changed replay content.
- Accepted documented one-time recurrence `none` and retained the `once` alias;
  execution now handles surrounding timestamp whitespace in existing schedules.
- Stop non-advancing directional history pagination with explicit incomplete/stop
  fields instead of returning a repeated cursor. Exhaustive export remains unproven.
- Preserve CLI two-step password bytes; only OTP input is trimmed.
- Reserve the Mini App signature field within the 64-field limit and reject all
  Unicode control characters in parsed/signed data and signing tokens.

- Added the project MIT license with copyright attributed to mimalef70.
- Consolidated installation, configuration and the endpoint directory in the README.
- Published the generated API contract as YAML; the hosted JSON URL remains available.
- Combined webhook integration and operations into two focused guides.
- Moved development invariants and acceptance gates into AGENTS.md, and coverage
  inventories beside protocol test data. Removed redundant implementation reports.
- Replaced historical comparison reports with a native capability inventory.

### Verified scope and remaining limits

- UI acceptance passed with synthetic accounts across Chromium, Firefox and
  WebKit, including Persian/English and the real Go REST stack. Linux arm64
  installation/restart was tested natively, Linux amd64 under emulation, and the
  packaged macOS arm64 binary was tested locally. macOS amd64 was cross-built.
- Core native messaging/media and selected group operations have evidence from
  two authorized Bale accounts. Version 1.0 does not expand that evidence to every
  API or establish a fifty-real-account production capacity.
- Ordinary-user keyboard-template sends remain rejected in live tests. Long-gap
  recovery, exhaustive history export and long-duration capacity remain acceptance
  gates. No financial mutation support or active-active deployment is claimed.
- Retention has no automatic cleanup. Back up data and the encryption key before
  upgrading to schema 5; rollback to older storage versions requires that backup.
- Account-security/report/story writes and Mini App credential interoperability
  retain their documented live-verification limits. See AGENTS.md and the native
  capability inventory for dated evidence.

## 0.2.0-alpha.1 — 2026-10-06

First public alpha: installation guides, a browsable API reference, contribution
and security policies, automated CI and prerelease packaging. Sponsored by MuChat.

- Canonical Go module and reproducible four-platform pure-Go release bundles.
- Pinned container base images and GitHub Actions.
- Updated Go text/crypto dependencies for current security fixes.

- Native Ogg Opus voice notes with validated framing and derived millisecond
  duration; account-scoped contact avatar downloads with bounded image validation.
  Voice receipt/download/display and current small/large avatars verified on authorized accounts.
- The 64 KiB text limit also applies to media captions before sends or schedules
  are persisted. Voice notes retain the existing durable/idempotent send contract.
- Native account/contact/privacy/session, group/channel permissions and history
  visibility, message reactions/pins/views, folders, polls, stickers and rich messages.
- Text/image story operations, provider Mini App RPCs and standalone HMAC helpers;
  capability coverage and live verification remain explicitly tracked per family.
- Rich received messages, quotes, service/group/presence events and bounded JSON
  contact/location decoding; corrected current auth timing and full-user schemas.
- Shared typed operation contracts drive validation, discovery and OpenAPI.
  Extended mutations use the durable journal; reviewed rich sends support schedules
  and exact own-message echo reconciliation. Media-backed changes pin their assets.
- Read-only sanitized wallet balances, journaled reporting/upvotes, strict input
  validation and no-store authenticated responses. No financial transaction claim.


## 0.1.0-dev — unreleased

Initial experimental GoBale gateway with an internal native Go `balemeow`
client. No public release or production-capacity claim has been made.

- Native Bale phone/code login, encrypted session restore, correlated WebSocket
  RPC, incoming updates and checkpointed recovery.
- Multiple isolated accounts, administrative REST/CLI, durable idempotent sends,
  explicit unknown outcomes, per-device signed webhooks and persistent schedules.
- Streaming media, reviewed messaging/history/contact/group operations, explicit
  errors for unsupported provider features and a route capability matrix.
- SQLite ownership protection, migrations, consistent backup/restore tests,
  bounded workers, authenticated metrics and non-root Docker builds.
- OpenAPI, integration examples, offline contract/race/fuzz tests and an optional
  synthetic 50-account soak harness.

Live verification includes two authorized accounts and bidirectional signed
webhooks. Remaining protocol and stability gates are recorded in
[AGENTS.md](https://github.com/mimalef70/gobale/blob/main/AGENTS.md#verification-and-release-gates).
