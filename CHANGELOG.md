# Changelog

## Unreleased

- Require explicit account selection and immutable instance headers on machine
  and browser APIs. REST sends, schedules and device provisioning require stable
  idempotency keys; update consumers with this contract change.
- Atomically provision devices and initial webhooks with a schema-7 journal;
  repeated requests preserve the original connection through restart/deletion.
- Add immutable connection identity to new event/webhook bodies and per-connection
  queue admission (default 100), with independent global limits and safe schedule
  deferral. Previously persisted delivery bodies keep their exact retry identity.
- Document backend channel ownership and provider-specific integration contracts; GoBale
  remains independent of consumer user/organization models.
- Add filtered operation/schedule/event queries, authoritative schedule executions,
  scheduled forwarding and explicit mentions in text/media captions. Mention and
  scheduled-forward provider interoperability remain live-unverified.
- Add per-device peer/sender/direction webhook filters and bilingual panel controls;
  preserve queued delivery targets and stable retry/replay identity.
- Schema 6 preserves durable outbox order and records new schedule occurrences
  atomically. Existing unrecorded occurrence history is marked incomplete.
- Add cached bounded operational metrics, request deadlines, safe panic responses
  and Docker log rotation. Keep SQLite WAL/FULL and one process owner.
- Clean only provably owned temporary uploads after failures/restarts; retain
  registered uploads and history without TTL. Avoid redundant voice spool copies.
- Add real process-crash, disk-full and read-only failure tests plus reproducible
  mixed/native synthetic capacity harnesses. A 300-account or 24-hour production
  capacity claim requires completed acceptance evidence; no new live claim or
  automatic backup facility is included.

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
