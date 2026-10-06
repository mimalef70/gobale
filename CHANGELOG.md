# Changelog

## Unreleased

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
