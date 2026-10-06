# GoBale development

## Scope and architecture

GoBale is an independent Go gateway for Bale accounts, with REST, CLI and signed
webhooks. Keep one module in `src` and the `cmd / ui/rest / usecase / domains /
infrastructure` layers. The embedded administrative UI lives in `ui/` and is served
from `src/ui/web`; there is no browser or Node runtime. Keep its scope to account
lifecycle, status and webhook administration; no chat or sending console. Consumers own
their users and permissions; do not depend on MuChat's models.

`internal/balemeow` owns transport, authentication, RPC and updates. It must not
import Fiber, SQL or webhook dispatchers. Use domain interfaces at the boundary.
REST handles HTTP contracts; usecases coordinate account lifecycle; storage owns
transactions and durable work.

The reviewed public Bale Web schema is `5.7.0+173855`. Protocol observations
are evidence, not runtime dependencies or a guarantee of current compatibility.

The project license is MIT in `LICENCE.txt`. Include that file when distributing
source, binaries or containers.

## Data, lifecycle and delivery invariants

- Scope sessions, peers, media, jobs and checkpoints to immutable connection IDs.
  Reusing a deleted alias must not inherit its old work. Reject account changes
  on a bound device; never fall back from an invalid explicit selector.
- Persist outbox work and request IDs before contacting the provider. Idempotency
  keys belong to the connection; changed content under a key is HTTP 409.
  Immediate sends and schedule creation share that namespace.
- Uncertain writes stay `unknown`; never blindly resend them. Only a reviewed
  proof tied to account, peer and persisted request ID can resolve ambiguity.
  Matching text or an unverified acceptance notification is not proof.
- Persist events, delivery records and checkpoints atomically. Advance checkpoints
  only after durable acceptance; duplicate events cannot rewind them.
- Socket connectivity, authentication and recovery are separate states. Expose
  incomplete recovery as degraded/gap information, not a synchronized inbox.
- Commit each schedule occurrence and outbox entry together. Use IANA calendar
  recurrence, including DST/month-end behavior; pin referenced media.
- Keep webhook event identity stable across retries/replay. Delivery is at least
  once, ordered per connection/destination, with independent destination queues.
  URL changes pause old pending work; only explicit replay selects new targets.
- Use SQLite WAL/FULL durability and one process owner. Bound queues, workers,
  RPCs and media transfers. Persistence failure must not become success.
- Logout/deletion cancels unsent work and removes local sessions; ambiguous work
  and audit history remain. Neither action implies complete data erasure.

## Protocol knowledge that must survive refactoring

- Use reviewed minimal `.proto` definitions, not the generated reference catalog:
  it contains incorrect scalar/message types. Keep literal-wire tests independent
  of the generated encoder for corrected layouts.
- Current auth and WebSocket endpoints both use `next-ws.bale.ai`. Handshake
  version 1 is distinct from app version 173855. Auth uses `auth-jwt` and cookies
  obtained by native login, with a cookie jar isolated per account.
- Bind restored cookies to configured endpoints. Do not follow auth redirects,
  copy browser sessions, guess alternate hosts or expose provider transaction
  hashes. An absent JWT is a protocol error, not proof that 2FA is required.
- Recognized password-required responses enter `awaiting_password`. OTP resend
  wait is field 8 in seconds; code lifetime is field 9 in seconds. Bound the local
  challenge to ten minutes or shorter provider expiry; do not conflate timers.
- `SendMessage` response field 1 is sequence-like; field 2 is a millisecond date.
  Message identity comes from the persisted request RID. Provider message/file
  IDs may be signed int64; generated request IDs remain positive. Public IDs are
  strings, never JavaScript-precision-limited numbers.
- Stream updates wrap a union with route/sequence/time metadata; edit date/editor
  use wrappers. Legacy Peer type 3 is encrypted-private, not channel. ExPeer has
  a different enum; legacy group peers need verified subtype metadata for channels.
- `GetFullUser` identity and `LoadFullUsers` supplementary metadata have different
  layouts. Resolve hashes through the selected account; never accept caller hashes.
- History modes are 1 forward, 2 backward(default), 3 both around an explicit date.
  Directional cursors use maximum/minimum returned dates respectively; both-mode
  bounds do not assert another page exists. Equal timestamp boundaries require
  deduplication and non-advancing-cursor protection; exhaustive export is unproven.
- Register private media references under the connection before advertising
  `download_supported`. Quotes cannot rebind another message's attachment.
  Keep signed URLs, access hashes and raw opaque data out of public JSON.
- Preserve real Unicode without a second unescape pass. Project only reviewed
  fields from nested content; bound depth, button counts, frames and batch sizes.
  Unknown variants remain explicit, not empty successful messages.
- State notifications can recur legitimately. Use actual route/time/sequence
  provenance; undated diff-page positions cannot invent per-update sequence IDs.
- Group permission patches preserve omitted and unknown fields. Replacement
  requires all known fields; read/merge/write has no provider compare-and-swap.
- Poll creation plus send is compound: an orphan poll or uncertain second step
  remains unknown. Story writes lack a wire request ID despite local journaling.
- Story IDs are opaque. Feed reads are not arbitrary-user inventories; text/image
  support does not imply video/widgets, media download or privacy-list editing.
  Financial read projections must not expose payment credentials.
- Some read-looking reference gift methods claim value. Financial mutations need
  a separate credential/reconciliation design; ordinary outbox rows are unsuitable.
- Voice requires complete single-stream Ogg Opus, mapping 0 mono/stereo, bounded
  packets and verified CRC/granules. Derive milliseconds from granules/pre-skip;
  no runtime transcoder or caller duration. Validate captions before enqueue.
- User avatars use fresh authenticated references and inspected JPEG/PNG/GIF.
  Cap compressed bytes at min(8 MiB, configured media limit), dimensions at 8192
  per side and 16,777,216 pixels. Decode GIF's first frame only; do not leak whether
  a missing avatar is private. Media fetch URLs require separate SSRF checks.
- Mini App HMAC helpers require a 32-byte hex hash and positive `auth_date`.
  Bound queries to 32 KiB/64 fields; reject duplicates (including encoded aliases),
  invalid UTF-8/encoding, controls and ambiguous names. Preserve raw field strings.
  Always check expiry: default MaxAge is five minutes, maximum seven days;
  future skew defaults to zero and cannot exceed five minutes. `expires_at` may
  only shorten validity. Compare HMACs in constant time; replay prevention belongs
  to the consumer. Locally signing data proves token possession, not provider
  login. Unverified parsing is never authentication; no unsigned launch fallback.

## Privacy

Never commit sessions, tokens, OTPs, real phone numbers, private messages,
databases, media captures or raw browser traffic. Decode captures, replace all
identifying values/content, then re-encode synthetic fixtures. Visible redaction
of opaque bytes is insufficient. Keep operational logs/metrics free of credentials
and message content; map free-form provider errors to reviewed public diagnostics.

Normal tests use fake servers and temporary databases. Live tests require identified
accounts and recipients under the operator's control; never use customer chats.
Distinguish schema observation, offline tests and live GoBale results. A source
method count or existing route is not a compatibility percentage.

## Verification and release gates

Development evidence recorded on **2026-10-06**, not a claim about later runs:

- Two real accounts verified native OTP, restart without OTP, bidirectional text
  and signed device-scoped webhooks. Short process-outage recovery passed; arbitrary
  long gaps and initial historical import remain acceptance gates.
- Core file/image/audio/video round-trips, edit/forward/delete of test messages,
  reply, scheduled send and basic two-account group lifecycle passed.
- Voice acceptance/receipt and byte-identical download passed independent Opus
  decoding; the web client displayed voice, but browser playback was not tested.
  Both avatar sizes and unavailable-avatar 404 passed for authorized test contacts.
- Poll send/results/vote/close, reaction, location, profile/folder/sticker reads and
  small forward/backward/both history windows passed; exhaustive pagination did not.
- Normal-user keyboard-template sends failed with `PROVIDER_INVALID_ARGUMENT`.
  Keep this unresolved. Account-security mutations, reports, story posting and
  Mini App credential interoperability were not live-tested; no financial test ran.
- Fifty simulated accounts exercised storage/webhook queues, not fifty native
  accounts. The documented 24-hour run was unfinished at its snapshot and used a
  frozen earlier binary. Do not infer completion or current status. Linux capacity,
  long-duration resource stability and deployment retention acceptance remain gates.
- Linux arm64/amd64 installation/restart smokes passed; amd64 was emulated on the
  development Mac. Cross-building macOS is not a runtime interoperability test.

Keep native capability coverage and known limitations in
[`capabilities.json`](src/internal/balemeow/testdata/coverage/capabilities.json).
Update this ledger only with recorded evidence, preserving date, scope and limits.

## Development checks

Use Go 1.26.6, Python 3.11+ and a C compiler for default SQLite/race tests.
Activate the project virtual environment and install `scripts/requirements.txt`
as shown in [CONTRIBUTING.md](CONTRIBUTING.md).
From the root run `make check`, `make race`, `make fuzz`, `make vuln`.
`make check` covers formatting, normal/purego tests, vet and contracts.
Run focused failure tests for lifecycle, parser, retry and account-scope changes.

Official builds need Node 24.12+ and `make ui-build` before Go compilation. Ordinary
Go checks must remain independent of Node and generated UI assets. Run `make
ui-check` and `make ui-e2e` for UI changes. Browser API routes are a finite allowlist;
use HttpOnly sessions, Origin/CSRF checks and immutable instance guards. Never put
credentials, OTPs or webhook secrets in browser storage. Build assets and their
manifest must match the binary version and OpenAPI digest; do not ship placeholders.

`docs/openapi.yaml` is the public API contract. Regenerate through
`python3 scripts/generate_openapi.py`; validate coverage with
`python3 scripts/check_capabilities.py` and keep `make contracts` green. Preserve
route/schema parity and error semantics. Do not regenerate removed prose reports.

After dependency/toolchain changes, run `go mod download` and `go mod verify` in `src`.
Wire regeneration uses `cd src && go generate ./internal/balemeow`; the reviewed
versions are protoc 7.35.1/protoc-gen-go 1.36.12. Recheck literals, bounds and fuzzing.
Keep soak outputs/private evidence in ignored `artifacts/`. Test Docker using
`scripts/docker_smoke.py` against a separate image and temporary volume.

## Release procedure

Set the version consistently in configuration, generated OpenAPI and the matching
`CHANGELOG.md` version section. The workflow extracts that section; do not create
another release-note directory. Preserve unresolved limitations in release notes.
Review staged files and secret scans before publishing. Pin Actions to
verified full SHAs; never execute unreviewed PR code with publishing credentials.

Push the reviewed commit and wait for CI. Tag that exact revision with
`vMAJOR.MINOR.PATCH` for an explicitly requested full release, or append
`-alpha.N`, `-beta.N` or `-rc.N` for prereleases, then wait for tag CI. Never move
a published tag. Dispatch `gh workflow run release.yml --ref main -f tag=TAG`.
Tag pushes alone do not publish. Local packaging uses
`python3 scripts/package_release.py --version VERSION --output dist` and must
exclude runtime/private data.

Verify the public release, tag SHA, four archives, manifest and checksums by
anonymous download. Verify anonymous GHCR pull, Linux amd64/arm64 manifest, immutable
digest, documentation and a clean temporary-storage restart smoke. Newly created
GHCR packages can be private despite a public repository; check visibility.
Local outputs or Actions artifacts are not public-installation verification.
Inspect partial drafts/images before retrying; never clobber a published release.
