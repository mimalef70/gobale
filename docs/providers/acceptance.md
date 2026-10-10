# GoOmni acceptance record — 2026-10-10

This dated record covers pre-release multi-messenger builds, identified below by
their exact checksums and source state. It is retained as protocol evidence for
GoOmni 2.3.0; it does not attribute these runs to the later release binary.
Release publication and anonymous installation checks are available in the
[v2.3.0 release](https://github.com/mimalef70/goomni/releases/tag/v2.3.0) and its
linked Actions runs. The six build gates and Docker restart smoke passed again after controlled
live testing exposed protocol defects. Limited Bale/Rubika live smokes passed;
Eitaa login, restart and self-text polling/webhook checks also passed; full provider acceptance remains pending. The native fixtures and
ten-minute combined synthetic workload below belong to the earlier, explicitly
identified pre-live build. Longer capacity runs remain pending.
The earlier GoBale 2.2.0 downloads do not contain the multi-messenger work.

## GoOmni rename and upgrade smoke — 2026-10-10

The local GoOmni 2.3.0 pre-publication binary
`19a3fd20fd5499e10824c8735c8dda0f8c6ec5baff656681643197c0cdaf77b2`
reused the previous gateway's exact database/media paths and master key after a
cold snapshot and process replacement. All six sessions restored authenticated
and connected without OTP; immutable connection/instance IDs and all 18 unknown
operation states/timestamps were unchanged. Fresh native text from account one
arrived exactly once on account two in each provider with the expected scoped
projection. Existing historical recovery warnings remained visible. Readiness
passed. This is a local dirty-tree build, separate from public release archives
and from the earlier one-hour recovery binary.

Explicit replay of one newly accepted test event per provider delivered all
three through `X-GoOmni-Event-Id` / `X-GoOmni-Delivery-Id` with valid HMACs and
the exact original body hashes. No historical body was reserialized.

The renamed source passed `make check`, `make race`, all nine `make fuzz`
targets and both `make vuln` variants, plus 42 UI unit tests and 33 browser tests
across Chromium, Firefox and WebKit. The non-root, read-only Linux arm64 image
passed fresh-storage restart smokes with the panel, a base path, and API-only
mode. These checks do not extend native operation or long-duration capacity
claims. Public Linux amd64/arm64 release installation checks run separately in
the linked publication workflow.

## Location, video and one-hour outage follow-up — 2026-10-10

This follow-up used the same two operator accounts in each provider, their direct
conversation and existing test groups. The macOS/arm64 gateway binary SHA-256 was
`73d3e0c4c44877aa93cd5d618d7af9a818b07a2d72ffee2808a92c7c2d9f0f5f`.
The [ledger](live-20261010.json) records its modified-source fingerprint and checks
separately from earlier binaries. No publication is implied.

The Eitaa location failure was a wire-alignment defect. The current official
Android `InputGeoPoint` has two doubles without flags. A controlled official-Web
request using its different catalog layout returned zero coordinates and used
latitude bits as the acknowledgement nonce. Native sends now use the reviewed
current layout, covered by an independent complete-send literal. Fresh sends in
both directions succeeded with correlated persisted nonces. Exact nonzero
synthetic coordinates appeared once in each recipient's incoming events and in
the official Web conversation. Earlier mismatched sends remain unknown.

Video preparation now uploads a real JPEG preview from a bounded Go AVC decoder
and sets streaming metadata. The preview's distinct upload ID is durably
journaled before the first upload. No duration, dimensions or audio are fabricated.

| Controlled Eitaa MP4 sample | Recipient presentation | Download |
| --- | --- | --- |
| Five seconds, with audio | Video, also visible in official Web | Byte-identical |
| Five seconds, silent | Video | Byte-identical |
| Two seconds, silent | Video; actual playback advanced in official Web | Byte-identical |
| One second, silent, original and fresh bytes | Ordinary file | Byte-identical |
| One second, with audio | Ordinary file | Byte-identical |

The official Web's one-second silent send also became a filename-only document.
This is an observed presentation limit, not a universal duration threshold or
arbitrary codec/playback support. Consumers must use the received media type.
Decoder limitations do not authorize fake thumbnails or a runtime transcoder.

The gateway process was genuinely stopped for **3,600.030 seconds**. The official
web clients sent 75 acknowledged test texts during the outage, at its beginning,
middle and end, including pages larger than Rubika's bounded first window.
The offline gateway's event/unknown counts remained unchanged before restart.

- All six native sessions restored without OTP. All 150 sender/recipient event
  copies were accepted: 36 Eitaa and 114 Rubika. None were missing, duplicated or
  assigned to another account/provider/peer.
- All 98 delivery records created for those events reached the configured signed
  receivers, with zero invalid signatures. Event and delivery counts differ
  because delivery creation follows the existing subscriptions/filters.
- Provider checkpoints did not rewind; all 18 pre-existing unknown operations
  retained that state. Recovery did not resend them or infer success from content.
- Each Eitaa account returned the same 64 unique direct-history messages using
  limit 100 (two pages) and limit 5 (13 pages). Each Rubika account returned the
  same 68 unique messages using its own offset contract (four and 15 pages).
  Every message from the pre-outage history baseline remained present.

An independent official-Web Eitaa traversal first returned 50 plus 13 messages;
the acknowledged final outage text made the native comparison 64. A provider
request for 100 returned only 50 with a placeholder count of 1000. Eitaa now uses
the observed 50-row wire profile, preserving continuation beyond that cap.
Rubika's runner follows `has_continue`/`next_offset_id`; its earlier comparison
incorrectly used Eitaa's cursor fields. That truncation was a runner defect.

Offline regression also covers an expired Rubika mutation state whose latest
interval must not overwrite an existing new-message watermark. Missing new
messages continue across pages and restart, while the warning about unproven
historical edits/deletions remains. This is synthetic expiry evidence, separate
from the real one-hour process outage.

`make check`, `make race`, all nine `make fuzz` targets and both `make vuln` scans
passed for the native changes. No reachable vulnerability was reported; one
required-module advisory affects no called code. Subsequent description-only
changes are checked separately. Private captures, sessions and identifiers stay
in ignored artifacts.

The final description-only build
`ead903e05ba14baa3d3739152a02c2e3d2ee3f17c2746d9c83a56ba8eb1c2e39`
updates the runtime video-format note without changing those native algorithms.
`make check` and `make race` passed again. Six sessions restored without OTP,
`/ready` returned 200, and fresh account-scoped Eitaa/Rubika history reads passed.
All 18 unknowns remained. The one-hour test belongs to the earlier checksum above.

The authorized test conversations and saved messages available here contain only
today's messages. Reading earlier messages from today and recovering a one-hour
process outage do **not** verify months-old archives, multi-day state expiration,
a network blackhole, lost historical edits/deletions or exhaustive initial import.
Existing gap/unclassified warnings remain visible. Those acceptance gates, Eitaa
polls and other recorded optional-operation limits remain open.

## Recovery and history follow-up — 2026-10-10

This follow-up separates Eitaa channel invalidations and unsupported update
coverage from an actual expired difference state. `updateChannelTooLong` requests
the independent channel stream; it does not by itself prove lost messages.
Account/channel checkpoints retain reviewed reasons for overlong differences or
updates whose message IDs cannot be bound to a peer. Old mixed warnings become
`legacy_unclassified` and remain visible. A failed RPC or storage callback is a
recovery failure, not fabricated evidence of an expired cursor. Empty and sliced
pages retain atomic checkpoint acceptance; a slice must advance provider state,
not merely the local checkpoint format or continuation flag.

Rubika now commits the bounded first `getMessagesInterval` window together with
its peer baseline. It previously discarded that window. Continued peer pages
rotate behind other pending peers, and a retained historical gap no longer
permanently disables subsequent new-message recovery. Initial attachment still
records `initial_history_imported: false`; this is not a complete inbox import.

History boundaries now come from delivered items. Rubika's inclusive `max_id`
requires the smallest delivered ID minus one; a server cursor beyond locally
truncated rows can skip messages. Eitaa native and official-Web reads with
`limit=5` returned a stale window, while the same peer with `limit=50` returned
the expected offset window. The gateway uses one bounded read of 50,
sorts and truncates locally, and returns the requested count with a scoped
cursor. Recognized zero-ID migration notices are counted separately from
messages. Neither adapter claims exhaustive pagination.

Controlled live results, with separate binary scopes in the
[ledger](live-20261010.json):

- After an orderly process stop, one message per provider was sent through the
  existing Eitaa/Rubika web clients to the other authorized account. After
  restart, each appeared exactly once in both the sender's and recipient's
  local events. This checks a short outage, not arbitrary long retention.
- A new private Rubika group had no peer checkpoint on either connection when
  the process stopped. Three first messages sent through the web client during
  the outage were all recovered exactly once on both accounts after restart.
- On the final binary, six native sessions restored without OTP. Six fresh
  directional text sends across Bale/Eitaa/Rubika each produced one matching
  incoming event. `/ready` returned 200. For each Eitaa/Rubika account, three
  five-message pages matched one fifteen-message page without duplicates.
- All 18 previously ambiguous operations remained `unknown` across these
  restarts. They were not blindly resent or marked successful by text matching.
- Bounded calls from the existing official Eitaa web client also rejected
  typing with `INVALID_CONSTRUCTOR` and poll sending with `MEDIA_INVALID`.
  Production gained no guessed constructor or automatic write retry.

Offline regression covers first-window commit failure/retry identity, foreign
peer rejection, rotation between continued peers, later messages after a retained
gap, unsorted/truncated history, cursor bounds, channel notices, unsupported
coverage, and gap provenance. `make check` and `make race` passed after the final
review changes. The nine fuzz targets and both vulnerability scans passed; their
parser/dependency inputs were unchanged by those final native review changes.
The matching OpenAPI/UI passed the 42-test UI unit suite and 33-test end-to-end
suite. No reachable vulnerability was reported; one required-module advisory
affects no called code.

The earlier failed small-page Eitaa comparison is retained alongside successful
retests. Existing historical warnings are not silently cleared. Full initial
history, long/expired gaps, remaining Eitaa poll/location/video-presentation and
optional-operation limits, second-factor variants and Linux capacity remain
acceptance gates for that earlier binary; the later location/video/one-hour
results above record their narrower acceptance. Private sessions, messages, identifiers and browser evidence
stay in ignored artifacts. These results do not supersede earlier failures or
broaden their recorded binary scopes.

## Common consumer contract follow-up — 2026-10-10

The [consumer guide](../consumer-integration.md#use-one-message-integration-across-providers)
now defines one backend boundary for Bale, Eitaa and Rubika. Newly accepted
message projections use `chat_id == peer.id`, full edits carry a stable
`original_message_id`, and Rubika partial edits use explicit field presence.
Old signed deliveries keep their accepted bytes. Invalid new projection identity
is rejected before event/checkpoint acceptance. Synthetic coverage includes
absent/empty text, historical decoding, restart byte preservation, scoped search,
conflicting patch identity and unsupported-field diagnostics without inventing
recovery gaps. Eitaa generic documents project the common `file` media type;
its encrypted native `document` download reference is unchanged.

On the exact matrix binary identified under `unified_consumer_contract` in the
[ledger](live-20261010.json), all six restored connections were authenticated and
connected without new OTP. Six directional text sends and six edits each yielded
one matching recipient event, with valid common identities; Rubika edits were
partial and Bale/Eitaa edits complete. Three replies retained the correct target.
Sender/recipient Eitaa message IDs differed as expected for account-local IDs.

Nine file/image/voice sends were received and downloaded. File and voice bytes
matched in all three providers; Bale/Rubika image bytes also matched. Eitaa
processed the 160×90 PNG into a same-dimension JPEG with a maximum one-level RGB
channel difference; it is not a byte-identical photo transfer. Browser playback
was not checked in this follow-up. The matrix snapshot recorded 628 signed
webhook deliveries with zero invalid signatures; this cumulative capture count
includes earlier controlled trials, not just these sends.

Bale typing/start-stop and Rubika typing were acknowledged; visual effects were
not tested. Eitaa rejected both typing and cancel in the reviewed current format
and in a bounded experiment with a documented non-threaded constructor. That
experiment was removed: production has one reviewed current format, no automatic
fallback and no active Eitaa typing preset. The source operation catalogue is not
a claim that this feature works on the deployed server. Read/delivered receipt
semantics, sender-name availability and unsupported presence are explicitly
reported by `interactions`, without synthetic acknowledgements.

After a final OpenAPI description/asset digest update, a separate final-build
smoke restored all six sessions without OTP, accepted three fresh text sends and
found exactly one correctly projected incoming event per recipient. `/ready`
returned 200. Native messaging logic was unchanged from the full matrix binary;
the final binary checksum and narrower smoke scope are recorded separately in
the ledger. `make check`, `make race`, `make fuzz`, `make vuln`, `make ui-check`
and the 33-test UI end-to-end suite are recorded in the private manifest. The
UI unit suite has 42 passing tests. The vulnerability scan found no reachable
vulnerability; one required-module advisory affects no called code.

Historical
Bale/Eitaa gaps and Rubika degraded/recovering states remain visible. Full initial
history, long gaps, all optional provider operations and capacity remain pending.
This follow-up does not supersede failures or earlier binary scopes below.

## Additional controlled trials — 2026-10-10

The follow-up source adds the corrections below; the local archive/container results
elsewhere in this record belong to their previously recorded binaries.

- Eitaa rejected the original non-square PNG group photo with
  `IMAGE_PROCESS_FAILED`. Explicit profile/group photo operations now inspect a
  JPEG/PNG source of at most 8 MiB, center-crop its actual pixels and encode a
  512×512 JPEG in Go. A new PNG group-photo trial passed. On an account with no
  pre-existing photos, profile-photo creation, ID listing and removal passed;
  the final photo list was empty again. Ordinary message images retain their
  original local bytes. Previous ambiguous photo operations remain unknown.
- Rubika member/admin/banned projections omitted `member_guid`. They now retain
  reviewed string IDs and member types, with duplicate/missing/type-mismatch
  rejection and no private avatar references. Live member/admin/banned reads
  passed after the correction. Ban/unban/add and secondary-account sending
  passed in the private test group.
- `avatar.list` now exposes only opaque Rubika avatar IDs and an explicit
  `complete: false`; it does not expose main/thumbnail download capabilities.
  Set/list/remove on an initially empty secondary profile passed and the final
  list was empty. This supplies the IDs required by the existing removal API.
- Eitaa album sending produced two recipient-side image messages with their
  captions. Classic-group creation and explicit supergroup migration, member
  add, ban/unban, default/member permission writes and subsequent secondary
  sending passed. A separate classic-group removal trial verified the secondary
  member absent after removal, present after re-addition and able to send.
  Channel signature/content-protection toggles were
  acknowledged and returned to their initial disabled settings. These
  acknowledgements do not prove every permission or forwarding effect.
- The final restart smoke exposed an Eitaa polling blockage after supergroup
  migration: `updateNewChannelMessage` carried a `messageService` migration
  notice with message ID zero. It now becomes a stable `chat.migrated`
  conversation event with the provider date/source chat and no fabricated
  message ID. Offline regression covers whole-page atomic acceptance, failed
  commit and retry identity. Live verification recovered the already-sent personal message exactly once
  from the previous checkpoint, without resending it. The active recovery error
  cleared; previously recorded gap markers were retained. A subsequent fresh
  three-provider text smoke passed with all six sessions restored without OTP.
- Eitaa reads an existing permanent invitation from a fresh account-scoped
  full-chat response before attempting export. Existing supergroup link lookup,
  preview, secondary-account leave/join and subsequent sending passed. This
  does not establish creation of a new invitation where none exists.
- Rubika contact cards sent with the sender’s own details passed in both
  directions, including a send without `user_id`. Received cards use
  `contact_message`, distinct from the write argument `message_contact`. The
  corrected bounded projection retains only reviewed contact fields and
  recipient-side exact-ID reads recognized the cards as supported contacts. A
  fresh card also appeared once in local events with reviewed contact fields.
  Earlier recipient-self and arbitrary-card failures do not establish a general
  contact-send failure; other card variants remain unverified.
- Rubika folder create/edit/list/remove and group preview/leave/join passed.
  The secondary account sent again after rejoining. Mute/unmute, pin/unpin and
  archive/unarchive were acknowledged; full notification behavior is unverified.

Failures of that earlier trial remain recorded: Eitaa polls returned `MEDIA_INVALID`;
location replies still failed request-ID correlation, including a privately
journaled small-nonce trial. Those sends remain unknown. Video presentation was
document-like; see the later follow-up above. Archive, fresh invite creation/export, slow mode, pre-join
history visibility and discussion linking returned `INVALID_CONSTRUCTOR` in
these trials. One unpin-all trial remained unknown; it was not retried. Earlier
Rubika contact variants returned `INVALID_INPUT`; the successful narrower trials
above supersede the general failure claim. No guessed fallback wire format or
content-based reconciliation was enabled.

Final follow-up verification: `make check`, `make race`, `make fuzz` and
`make vuln` passed on the corrected native source. The same UI/OpenAPI passed
`make ui-check` and `make ui-e2e` before these final native-only changes. The
final binary restored six authenticated sessions without OTP; all three fresh
text sends succeeded and each appeared once in the selected recipient’s local
events. Signed webhook observations had zero invalid signatures. This is a
local macOS, two-account-per-provider trial, not Linux capacity or complete
recovery acceptance. The initial failed smoke is retained in private evidence
alongside the later successful run.

An existing secondary Eitaa profile photo was left untouched. Second-factor
variants need accounts already configured for those variants; these trials do
not alter account security. Sticker/GIF cases without suitable provider assets,
remaining profile/public-username mutations, exhaustive recovery and capacity
acceptance remain pending. Per-operation responses are recorded in the
[response ledger](live-20261010.json); an acknowledgement is not full end-to-end
verification. Private captures, identifiers, sessions and test messages remain
in ignored local artifacts.

## Evidence boundaries

- **Source observation:** pinned MIT references and inspected public provider
  bundles establish reviewed wire behavior. They are not live compatibility
  results. See [source provenance](sources.md) and the provider inventories.
- **Offline implementation:** fake servers, temporary databases, literal-wire
  fixtures, migration/fault tests and browser tests exercise gateway behavior.
  Native client fixtures still talk to local protocol servers.
- **Live evidence:** the dated Bale ledger retains its previously recorded
  two-account results and limitations. The controlled live smoke below records
  narrower new Bale/Rubika/Eitaa evidence and their remaining limits.
  No test in this document establishes a real-account quota.

The implemented core selects an immutable provider for each connection and
shares durable admission, scheduling, local event queries, signed webhooks,
bounded workers and operational diagnostics. Sessions, private peer/media
references, checkpoints and operations remain scoped to that connection.
Eitaa and Rubika are opt-in; Bale remains enabled by default. Consumers retain
their own users and permissions. The panel remains limited to administration.

Schema 8 preserves existing Bale sessions, retained events, exact signed delivery
bytes, queued/unknown operations, schedule occurrences and private references.
Provider envelopes and media ordering metadata preserve the original encryption
binding. Migration and restart tests cover this preservation; upgrades still
require the documented cold backup and one process owner. No automatic history
retention or PostgreSQL migration is included.

Bale's existing native behavior is retained through the provider boundary and
covered by regression tests. This does not renew the dates or broaden the scope
of its [live capability ledger](../../src/internal/balemeow/testdata/coverage/capabilities.json).

## Controlled live smoke after protocol fixes

### Subsequent two-account verification on the same date

Two independently authenticated, operator-controlled accounts per provider were
used for direct messages and newly created private test groups/channels. No Bot
test was requested; account-security mutations, financial operations and reports
remain outside this acceptance scope. The per-operation
[response ledger](live-20261010.json) distinguishes read responses, write
acknowledgements, unsuccessful attempts and operations not run. An acknowledgement
does not establish the full effect of a mutation or a compatibility percentage.

- All six native sessions restored without OTP. Bidirectional direct text reached
  the selected recipient's local event query. Group/channel creation and scoped
  history were exercised for all three providers; Bale's initial invitation was
  privacy-rejected, then the second account joined through the test invite link.
- Replies, edits, forwards, pins/read operations, test-group administration and
  one-time schedules were exercised. All six text/forward schedule occurrences
  reached `succeeded`; this does not establish long-term recurrence coverage.
- File, image, audio, voice and video were downloaded from the recipient account.
  Bale bytes matched the originals. Rubika file/image/voice/video bytes matched;
  music required inspected MP3 with integer seconds, while voice retained Ogg
  Opus. Image/video previews are generated from actual decoded pixels in Go.
  Browser rendering was observed; exhaustive playback/format coverage is pending.
- Eitaa file/audio/voice bytes matched. Its image was converted to JPEG by the
  provider. Video bytes arrived, but the provider returned a document without
  video attributes: native video presentation is not verified. Thumbnail and
  streaming-flag trials did not resolve this and were removed from production.
- Eitaa location history exposed a missing reviewed `geoPoint_84` layout; native
  projection and an independent literal-wire regression now cover it. Location
  sends still returned a mismatched provider random ID and remain `unknown`.
  They were not reconciled by matching text/content. Eitaa poll sends were
  rejected with `MEDIA_INVALID`; the original test-group photo change remained
  `unknown`. A later corrected photo trial is recorded below; it does not
  reconcile or retry that original operation. Initial Rubika contact variants were rejected; later sender-card trials and corrected receive projection passed as recorded above.
- Eitaa server `INVALID_CONSTRUCTOR` responses now map to a bounded unsupported
  diagnostic. Observed examples include contact status/IDs/saved-contact and
  discussion-group methods. Their presence in the schema is not live support.
- Test-group photo changes and invite-link rotation were acknowledged by Bale
  and Rubika. Rubika reaction removal now omits `reaction_id`, as the official
  web client does; add/remove both succeeded after this correction. Reaction
  identifiers are strings. Global message search now projects the provider's
  outer result timestamp and passed on the controlled account; single-peer
  lookup passed, while the earlier mixed-peer lookup was rejected.

Actual `SIGKILL` outage testing found two recovery defects. Bale now refreshes the
route inventory on restore and starts newly discovered routes at zero, retaining
existing cursors and any prior gap marker. Rubika now accepts sparse edits and
separately pages new message IDs instead of treating mutation state as a new
message cursor. Synthetic tests cover atomic acceptance, failed commits,
cross-peer rejection, duplicate boundaries and non-advancing pages.

After those fixes, a new message sent through each official web client while the
gateway was stopped appeared exactly once in the second account's local event
query and reached the receiver with a valid signed webhook. All twelve previously
unknown operations remained unknown across the crash/restart. This is a short
process-outage result, not long-gap, initial-history or 300-live-account acceptance.
Existing Bale/Eitaa gap diagnostics and Rubika's unverified overall recovery
status were retained; this test does not certify a synchronized entire inbox.

On this final source, separate Linux/arm64 native fixtures with 100 clients per
provider also passed against local fake protocol servers (4 CPU / 8 GiB per
fixture). Bale recorded 600 RPCs and 100 updates; Eitaa recorded 500 RPCs, 200
updates and 300 checkpoint commits; Rubika recorded 400 RPCs, 200 updates and 200
checkpoint commits. These are three independent fixtures, not a single mixed
300-account production deployment. Their binary checksums are in the response
ledger. All six development gates passed, along with the isolated Docker restart
smoke and four locally verified installation archives. The native macOS/arm64
archive additionally restored all six real sessions and sent a text per provider
that appeared in the selected recipient's local events. No publication occurred.

The remaining limitations above and every `not_run` ledger entry are pending
acceptance. Neither all source methods nor all advertised operations have passed
live end-to-end verification. Private raw evidence and account data stay in the
ignored local artifact directory.

Tests on 2026-10-10 used one identified operator-controlled account per provider,
with initial writes restricted to each account's self conversation. A subsequent
Eitaa test targeted an explicitly operator-authorized support recipient. Native sessions were
created with provider OTPs, independently of the official web browser sessions.
No customer conversation was used as a write target. Live recovery can read the
selected account's update stream; this is not a complete-inbox acceptance test.
Private identifiers, codes, tokens, messages and captures are excluded from Git.

| Workflow | Bale | Rubika | Eitaa |
| --- | --- | --- | --- |
| Native OTP login | Passed | Passed, including native device registration | Passed after correcting the authorization TL layout |
| Restart without another OTP | Passed | Passed | Passed |
| Self text send | Passed | Passed, including unchanged-key replay | Passed |
| Message from the official web self chat → durable event → signed webhook | Passed | Passed | Not reached |
| Local event text search | Passed | Passed after query correction | Passed for the synthetic self text |
| File send → history registration → byte-identical download | Passed | Passed | Not reached |
| Ogg Opus voice send → byte-identical download → independent decoding | Passed | Passed | Not reached |
| Scheduled self text and successful occurrence | Passed | Passed | Not reached |
| Self edit and forward | Not repeated in this smoke | Passed | Not reached |

Rubika's official web voice control advanced to the end of the synthetic
0.6-second sample. Bale voice was rendered as voice in the web UI; browser playback
was not repeated. Downloads were requested only after the account-scoped history
query registered a private attachment reference. Before that registration the
API returned 404 rather than exposing an unregistered provider reference.

The initial live fixes corrected Rubika socket discovery and native device
registration. The subsequent personal-conversation correction preserves Bot
and Service namespaces throughout shared conversation handling instead of
skipping them. A read-only Service `chat.info` and history request returned
three supported text messages on the operator account. No Bot chat was present;
the operator explicitly excluded Bot live testing because their use case is
personal conversations. Bot/Service mutation variants remain live-unverified.

A new self message sent through the native API succeeded for Bale and Rubika.
A message sent from Rubika Web Saved Messages then appeared in the durable local
event search and reached the self-chat-filtered receiver with a valid HMAC.
All 35 deliveries observed by that receiver through this stage had valid
signatures. This repeats a controlled self-chat check, not a second-account
inbound test or complete-inbox acceptance.

Unsupported namespace/content coverage is independent of recovery. Only provider
`OldState` creates a proven `chat_state_expired` or `message_state_expired` gap;
transport/persistence failures expose diagnostics without inventing expired
state. The previous checkpoint's ambiguous flag remains visible as
`recovery=degraded,recovery_issue=legacy_unclassified`. It is not silently cleared
or represented as proven loss. Checkpoint format 2 discovers previously skipped
peers while preserving the existing account cursor. A restart decoder default
that could incorrectly restore an omitted `listing=false` as true was corrected
and covered by upgrade tests. These are provider checkpoint format changes,
not a new SQLite schema migration.

Local text/caption search uses the reviewed Eitaa/Rubika message projection and
excludes quoted/private data. Synthetic regressions cover Bot/Service actors,
account isolation, delivery filters, failed acceptance and proven/legacy gaps.

Eitaa's controlled authorization response exposed an omitted terminal TL bytes
field under authorization flag 10. The exact optional layout is decoded with
independent literal/truncation tests; the field is private and its semantics
remain unreviewed. Whole-response trailing validation remains mandatory.
The official worker's `INVALID_LOGIN` response now enters bounded token renewal,
which persists replacements before use and never retries uncertain writes.
A private attempt to validate the earlier capture with a newly generated client
identity was rejected; it does not establish working session restoration.
A fresh native OTP login subsequently succeeded with its original native client identity.
Profile/dialog reads and restart without another OTP passed. The same persisted
session passed profile reads with both the diagnostic envelope profile `128` and
the production profile `32`; the ordinary production profile was therefore retained.
A production self-text send succeeded, was accepted by the update poller, appeared
in scoped local event search and reached the self-chat-filtered receiver with a
valid HMAC. History was initially empty and returned the synthetic message on a
subsequent read; this does not establish exhaustive pagination. Eitaa retains its
documented degraded recovery status and no initial-history completeness claim.
Across the receiver at this stage, all 39 deliveries had valid signatures.
This is self-chat update evidence, not another-user inbound, 2FA, media or long-outage acceptance.

A subsequent production Eitaa text to the operator-authorized support recipient
succeeded and was verified in provider history, the durable scoped event query
and a valid signed webhook. The receiver had accepted 40 deliveries, all with
valid signatures. No reply had arrived at this checkpoint, so this does not yet
verify another-user inbound delivery. No recipient identifier or private content
is recorded in public evidence.

Initial live diagnosis used an instrumented local build, isolated from ordinary
installation data. A production source build without those diagnostics then
passed Bale/Rubika session restoration, local search and voice transfer/decoding.
All six build gates and isolated Docker restart smokes passed on the post-fix
source. The exact post-fix identities are:

| Artifact | SHA-256 / identity |
| --- | --- |
| Source/test/assets | `9d11a048c39676cacdc3f858772dd8f579f0a71195b8814b72bff0998bce5ec9` |
| Non-instrumented macOS/arm64 live-test binary | `333c8d406806befc30440ebc582257a8d06cf7c5e992efcf7a39c563ea7eecd9` |
| Linux/arm64 local Docker image | `sha256:71d8f435ba299a193977b71ea5fd71fed83abd0c587a64dbc9adabce759d4eb7` |

The table above describes the earlier protocol-fix build, before the subsequent
personal-conversation correction. Its OpenAPI and UI digests match the older
capacity table below. Subsequent build identities are recorded separately. Post-fix
private test manifests/logs live under ignored
`artifacts/live-peykbridge-20261010/`; public inventories contain only the dated
scope and limits. The repeated post-fix mixed-300 run did **not** start: its disk
preflight rejected insufficient Docker-volume space plus the required reserve.
The earlier passed run below remains evidence only for its earlier source.
No data or disk reserve was removed to turn that blocked run into a pass.

## Personal-conversation correction build identity

All six development gates passed on the personal-conversation correction source.
The final production binary also restored the Eitaa session created by native
OTP without another code and passed the self-text/polling/webhook check above.

| Artifact | SHA-256 / identity |
| --- | --- |
| Source/test/assets (src, ui, scripts, docker; embedded assets included) | `a9cac023cd773b69f6b06e022457bdd6f3c218e85f37f27c7235eee0aed206e6` |
| Non-instrumented macOS/arm64 binary | `d3e23adf236d7bcae82e09bfbfd2218c2601bdaa94c4d1d32c61481f7570eb62` |
| OpenAPI | `6d3b0825bb93f4fa77dab11ce2685ec7d7ae5fab84a052b4e103bc98f6a74e31` |
| Embedded UI manifest | `3837db528854fe881a3c1dfc3fd05021ac98e84c44091c8b7bbd155fd055b1b4` |

The source remains uncommitted on base revision
`d3453cfba43a4670a2a55e29f1489e37ef07de04`; this is local evidence, not a
published release. No current 300-client or long-duration run is claimed.

## Pre-live capacity build identity

| Evidence | Recorded value |
| --- | --- |
| Git revision and working-tree state | `d3453cfba43a4670a2a55e29f1489e37ef07de04`, with uncommitted implementation changes |
| Source/test/assets SHA-256 | `3a0ce4e09aa511716f2e5a64c8e5f12b6b9bfc19ddf515633b96c07e8c4a20af` |
| Docker application binary SHA-256 | `db8d13e74f0071b20db5ae4e2c0c3e7a5faf57feb6861610419d0e67f0cd0fb9` |
| OpenAPI SHA-256 | `c6890e907403cc617f392a696de6d3c3594ab47aa9dc32c0cb1aaeebec51ffa5` |
| Embedded UI manifest SHA-256 | `1d25d7477d4feafadaf1b986acef1b1814b3e6fc05dcbfc13a0b96bb298f0396` |
| Local image ID | `sha256:51f5aba7758b8fd1a289068f6ab3dc65f700bbd2ea25bbdc49e981c231473f23` |
| Fixture platform and resources | Linux/arm64 in Docker Desktop, 4 CPU, 8 GiB, GOMAXPROCS=4, external networking disabled |
| Bale native fixture SHA-256 | `45695b99210208705ea4dc17828f180dc52506c509b3c42d9ed7bdde62e225b6` |
| Eitaa native fixture SHA-256 | `b7b0e8dbf242f3fb52baa4ca0afcdbd67d9b11584860d5c0146228a6bf2736d0` |
| Rubika native fixture SHA-256 | `c44d1c6e52de2cce7adb7e70f87b5cfad7e05f5afce518062ca95e7b2240d7fe` |
| Mixed service/storage fixture SHA-256 | `1e952f77786a391f083f51541945d022857ac78a462bd8b574197bc1a66e35fc` |
| Mixed fixture seed | `1` |
| Local archive manifest/checksums | `dist/peykbridge-local/manifest.json` and `SHA256SUMS`; four platforms, configured version 2.2.0, dirty source tree |

Record the exact binaries used by each test. The service/storage harness and
native protocol fixtures are separate executables; their checksums must not be
presented as the application binary's checksum. Operational evidence belongs in
ignored `artifacts/`, without credentials or real messages.

## Required build and regression gates after live fixes

| Command | Scope | Post-fix result |
| --- | --- | --- |
| `make check` | Formatting, normal and pure-Go tests, vet, generated contracts | Passed |
| `make race` | Concurrent Go paths, including lifecycle/storage/provider fixtures | Passed |
| `make fuzz` | Seven bounded protocol, voice and Mini App fuzz targets | Passed |
| `make vuln` | Default and shipped pure-Go dependency scans | Passed |
| `make ui-check` | Type checking, lint and UI unit tests | Passed |
| `make ui-e2e` | Browser administration against isolated fake accounts | Passed |

The personal-conversation correction passed 42 UI unit tests and 33 browser tests across Chromium, Firefox and
WebKit. The vulnerability scans found no reachable or imported-package
vulnerabilities; they reported one advisory in a required module whose affected
code is not called. This is a scan result for this build, not a future guarantee.

Run from the repository root with the documented Go, Node and Python toolchains:

```sh
make check
make race
make fuzz
make vuln
make ui-check
make ui-e2e
```

Focused tests additionally cover account/alias replacement, private-session
rotation and logout, atomic event/checkpoint batches, uncertain sends, compound
operation stages, schema upgrades and process-crash boundaries. Fault tests are
synthetic; they are not evidence that arbitrary real-provider outages recover
completely.

## Pre-live Linux protocol and service/storage acceptance

| Run | What it exercises | Pre-live result |
| --- | --- | --- |
| Bale: 100 native clients | Local WebSocket, restored synthetic sessions and reconnect | Passed: 400 RPCs, 100 updates; initial connect 2.605 s, reconnect 2.565 s, four workers |
| Eitaa: 100 native clients | Local TL authentication, polling, acceptance callbacks and reconnect | Passed: 100 logins, 200 connections, 500 RPCs, 200 updates, 300 checkpoint commits; at most four shared permits |
| Rubika: 100 native clients | Restored synthetic credentials, encrypted HTTP and local socket reconnect | Passed: 200 handshakes, 400 RPCs, 200 updates, 200 checkpoint commits; at most four shared permits |
| Combined service/storage: 300 connections | 100 per provider using synthetic adapters, real REST, SQLite and signed HTTP webhooks | Passed: 600 seconds of load, 43,153 completed events, 7,202 completed sends; backlog drained in 1.080 s |

The three native runs are separate. They must not be described as a combined
300-client native run. Restored-session fixtures do not exercise a real OTP or
prove provider login interoperability. The combined workload tests shared queue,
storage and webhook behavior; it does not exercise the three real transports.

The mixed smoke measured 600 seconds after setup, with no warm-up. Baseline load
was 60 incoming events/s and 10 new sends/s (8 immediate, 2 scheduled), plus a
threefold 60-second burst. Read/search traffic was 10 requests/s and uploads were
1 MiB every ten seconds. Four send, eight webhook, four reconnect and four media
workers were configured, with 500 ms polling and queue caps of 1,000 globally
and 100 per connection. Every connection had a healthy webhook destination;
30 also had a 150 ms destination and 30 a destination returning a single 503
on every eleventh event before succeeding on retry.

| Work lane | Offered | Admitted | Rejected | Generator misses |
| --- | ---: | ---: | ---: | ---: |
| Incoming events | 43,153 | 43,153 | 0 | 46 |
| Immediate sends | 5,761 | 5,761 | 0 | 0 |
| Schedule occurrences | 1,441 | 1,441 | 0 | 0 |
| Read/search requests | 5,999 | 5,999 | 0 | 1 |
| 1 MiB uploads | 60 | 60 | 0 | 0 |

All 43,153 admitted events reached the healthy receiver and all 7,202 admitted
sends completed. Account/provider identities and per-destination order checks
passed. No duplicate receiver delivery or unexpected failed/unknown operation
was observed in this workload. This result does not establish exactly-once
webhooks; separate fault tests cover replay after ambiguous receiver commits.

| Measurement | Recorded result |
| --- | ---: |
| Durable admission p95 / p99 | 5 / 20 ms |
| Healthy destination p95 / p99 | 500 / 1,000 ms |
| Backlog drain after production stopped | 1.080 s |
| Maximum process RSS | 43,294,720 bytes (41.3 MiB) |
| Post-GC heap, initial → final | 1,795,272 → 1,390,248 bytes |
| File descriptors, initial → final | 22 → 20 |
| Goroutines, initial → final | 57 → 28 |
| Maximum test disk use | 186,724,128 bytes |
| Measured disk growth | 180,096,080 bytes |

Percentiles are histogram bucket upper bounds. Healthy latency excludes the
burst interval; impaired destinations are exercised concurrently. The process
includes the bounded synthetic harness. The host used Docker Desktop's
Linux/arm64 runtime with enforced 4 CPU / 8 GiB limits and no external network;
these numbers are not a native production-server benchmark or live-account test.

The complete local records are `artifacts/soak/peykbridge-soak-mixed300-20261010-final/run.json`
and `result.json`, `artifacts/provider-fixtures/final-native/manifest.json`, and
`artifacts/peykbridge-local-verification/verification.json`. They retain exact
commands, binaries, resources, source fingerprint and completed verdicts.

Reproduce the mixed smoke and the separate Bale native fixture:

```sh
docker build --file docker/golang.Dockerfile --tag goomni:dev .
python3 scripts/start_soak.py --image goomni:dev --providers bale,eitaa,rubika --accounts 300 --duration 10m --warmup 0s --max-disk-gib 1 --wait
python3 scripts/start_soak.py --image goomni:dev --native --accounts 100 --duration 1s --warmup 0s --max-disk-gib 1 --wait
```

For Eitaa and Rubika, compile separate Linux fixtures, then run each against its
own local fake servers. Select the architecture of the Docker runtime; use
`amd64` on x86-64 or `arm64` on ARM64. These commands do not contact messengers:

```sh
CAPACITY_ARCH=arm64
mkdir -p artifacts/provider-fixtures
for provider in eitaa rubika; do
  (cd src && CGO_ENABLED=0 GOOS=linux GOARCH="$CAPACITY_ARCH" go test -tags purego -c -o "../artifacts/provider-fixtures/$provider.test" "./internal/${provider}meow")
  docker run --rm --network none --cpus 4 --memory 8g \
    -e GOOMNI_MIXED_CAPACITY=1 \
    -v "$PWD/artifacts/provider-fixtures:/fixtures:ro" \
    --entrypoint "/fixtures/$provider.test" goomni:dev \
    -test.run '^TestRunConcurrent100NativeClients$' -test.count=1 -test.v
done
```

Record source fingerprint, checksums, image ID, architecture, resources and the
completed output alongside these manual fixture runs. For a detached mixed run,
collect its recorded verdict with
`python3 scripts/start_soak.py --collect artifacts/soak/RUN/run.json`.

The existing `python3 scripts/run_capacity.py` runs the **Bale** comparison and
long-duration sequence. It does not automatically run a three-provider sequence.
The 50/150/300 comparison, ten-minute warm-up plus one-hour measurement, and
24-hour final-binary soak are **pending** in this record. A passed short smoke
does not satisfy them. The 24-hour disk budget must use a passed one-hour growth
measurement with the documented reserve; insufficient space means not run, not
passed. See [capacity operation details](../operations.md#reproducible-capacity-acceptance).

## Remaining provider limits

- Initial attachment records a baseline; it does not import the complete inbox.
  Exhaustive history export, arbitrary long-gap recovery and complete event
  coverage remain unverified.
- Mentions are Bale-only and remain live-unverified. Eitaa/Rubika reject nonempty
  mentions. One-time scheduled forwarding was acknowledged in the two-account
  trials above; recurrence/forwarding variants remain unverified. Other operations are
  schedulable only when their selected-provider capability explicitly says so.
- Eitaa's peerless delete/content updates stay unresolved and gap-marked. Separate
  poll-result/vote updates lack the peer binding needed for scoped projection.
  Sticker/GIF/reaction paths blocked by the reviewed web client's behavior are
  not advertised as server capabilities.
- Rubika does not advertise `poll.close`, mixed-media album sending or incoming
  read-receipt projection. Conflicting attachment edits without reviewed ordering
  evidence withhold download availability rather than expose stale bytes.
- The new adapters have bounded media-format support, not runtime transcoding:
  voice uses inspected Ogg Opus. Eitaa audio uses Ogg Opus; Rubika music
  uses inspected MP3 (at least one second). Video uses inspected unfragmented AVC
  MP4; Rubika additionally requires a decodable first IDR frame for a real thumbnail. Their capability responses describe supported ordinary sends.
- Eitaa/Rubika uncertain writes remain `unknown`. Automatic reconciliation is
  restricted to Bale's reviewed account/peer/request-bound proof; a socket event
  or matching text is not sufficient.
- New financial/security mutations, Bot APIs, Rubino and real-time call engines
  are outside this expansion. Existing Bale exclusions and live-test limits
  remain in its ledger.

## Packaging, publication and remaining acceptance

The post-fix Linux/arm64 Docker build and all three isolated restart scenarios
passed: UI at the root path, UI under `/gateway`, and API-only startup. They
verified readiness, retained connection data, a non-root/read-only runtime and
administrative-session invalidation on restart. Four local archives for Linux
and macOS (amd64/arm64) are prepared under `dist/peykbridge-live-check`; their local
manifest and `SHA256SUMS` record inventory and byte verification. These are local
builds of an uncommitted source tree at configured version 2.2.0. No tag,
public release, GHCR/Docker Hub publication or anonymous registry verification is
claimed by this task. Repository and published package URLs remain unchanged.

Before describing Eitaa/Rubika as live-verified, use identified operator-controlled
accounts and recipients to check login/password where applicable, restart,
bidirectional messaging, media, selected group/channel operations, outage
recovery, durable unknown outcomes and account isolation. Do not use customer
chats. Record each result and its scope separately. Full provider live acceptance,
the one-hour target workload and the 24-hour soak remain **pending** until their
own final evidence exists.
