# Native provider source provenance

Source observations and synthetic tests do not establish current provider
interoperability or live account capacity. Each native adapter has a finite
allowlist; the complete extracted schema is never an arbitrary public RPC API.

| Reference | Immutable revision | License and use |
| --- | --- | --- |
| [eitaa-cli](https://github.com/EhsanAhmadzadeh/eitaa-cli) | `685a71e12d78cf58cb18074e08aad4c8e06981a0` | MIT; bundled TL schema and reviewed protocol behavior |
| [Eitaa Laravel serializer](https://github.com/disintegrations/eitaa-serializer-laravel) | `d19111fe518e60e23fd52cc902afb3e9d51c9256` | MIT; independent schema/media comparison |
| [Rubingo](https://github.com/farshadnobody/Rubingo) | `39f780813e558f390a9404964e1d9ec80748e66f` | MIT; native Rubika protocol reference |
| [Rubx](https://github.com/Mester-Root/rubx) | `717e7b82046bfb0813d90812c48289a1bc6a82ce` | MIT; older Rubika protocol comparison |
| [Rubpy](https://github.com/shayanheidari01/rubika) | `af0d5c05304aee4d50ca018becbb70881cf5e2e5` | GPL-3.0; behavior and inventory observation only, no copied implementation |

The Eitaa schema is embedded from `eitaa-cli`'s
`src/eitaa_cli/data/eitaa-schema.json`. Its SHA-256 is
`4395de6e5d72333679a483d047de4f20fc1b3e98e073e1c9d5a20e8f4275f5d9`.
The original MIT notice is retained beside the embedded schema in
`src/internal/eitaameow/schema/LICENSE`.

The SRP implementation follows the published equations at
[Telegram's SRP documentation](https://core.telegram.org/api/srp) for the
explicit KDF constructor found in Eitaa's pinned TL schema. Successful synthetic
SRP tests do not establish Eitaa password-login interoperability.

## Evidence and gaps

The JSON inventories distinguish source observation, native implementation,
remaining variants and explicit exclusions. A schema entry or reference wrapper does
not imply a tested public operation. Actual supported admission contracts are
returned by each adapter's `Contract.Operations` implementation.

Eitaa peer IDs are opaque. Classic chats use a decimal group ID; channel-backed
supergroups use `channel_<decimal>` under peer type `group`. Broadcast channels
retain type `channel` and a decimal ID. Distinct wire namespaces therefore cannot
share a cache key, media key or cursor even when their numeric IDs coincide.
Do not strip the namespace or reuse an ID under another peer type.

Eitaa is a TL-over-HTTPS provider. Its account and channel difference cursors must
commit with their event pages; a callback failure must not advance local state.
First attachment starts at a recorded baseline and does not import all history.
Unknown updates and overlong differences keep recovery degraded or gap-marked.
The `incoming_updates` inventory is separate from RPC methods: exposed writes
and schema constructors do not imply complete receive-event coverage.

Rubika uses encrypted HTTP method calls and a separate update socket. Provider
nonces must be persisted before transmission; receiving a similar message or a
socket notification is not sufficient proof to reconcile an uncertain send.

New financial/security mutations, Bot APIs, Rubino and real-time call engines are
outside this expansion. Existing Bale behavior is preserved separately. Native
account creation is not an automatic response to a signup-required login result.

Limited operator-controlled live results are recorded in [acceptance](acceptance.md).
They do not establish full provider coverage. Remaining live gates include
second-factor variants, disconnect recovery and cross-account isolation.

## Reviewed public web observations (2026-10-10)

These public first-party bundles were inspected as protocol evidence. Their code
is not bundled, copied into adapters or executed at runtime:

| Provider | Public artifact | SHA-256 |
| --- | --- | --- |
| Eitaa | [worker](https://web.eitaa.com/mtproto.worker-Zk-UnVZ2.js) | `ef186db9dfa2a1e79ad30aadd805a5d455e90ee66b2827feca8006df979f4c17` |
| Eitaa Android | [official APK](https://eitaa.com/app/apk) | `55dfe3da6eaa368aa680a15ad6f8e899969d868e56354d9a8a51f58e09e60c92` |
| Rubika Web | [main bundle](https://web.rubika.ir/main-es2015.6421623571994c9dd619.js) | `47b302b9166ed8c9138f9d959e9107c53dcb78c9447f8281715f657b68080acb` |
| Rubika PWA | [main bundle](https://m.rubika.ir/static/js/main.4fda2e41.js) | `8d91bd40a97fe7bfba1314f540244d0966b095ea869d1febca04eac0ff0f8444` |

The Eitaa worker clarified authenticated token replacement and app identity. Its
`account.password2` login branch computes SHA-256 of salt, UTF-8 password and salt,
then calls `auth.checkPassword2` with the retained login phone/code. The native
implementation handles this explicitly; SRP is selected only by its distinct
`account.password` constructor. Delivery/next-delivery metadata comes from the
actual response, including app delivery; it is never inferred from requested SMS.
Rubika similarly maps returned `send_type` (`SMS`, `Internal`, `CallCode`). A
replacement token must commit before the client activates it. An interrupted
mutation remains unknown and is not repeated after renewal. The MIT Laravel
reference documents upload-envelope flags `128` and the upload endpoint for the
entire upload transaction; the inspected official worker also uses flags `128` for ordinary native HTTPS requests. A controlled fresh login and profile read passed with `128`; after restart, the same persisted native session also passed a profile read with the production default `32`. The ordinary `32` profile from the pinned CLI is retained; this observation does not establish equivalence for every operation.

The Eitaa worker also contains an explicit `eitaaNoSend` table. Affected
sticker/GIF collection methods, contact ranking and other listed methods return
local stubs without a server call. Their presence in the TL schema is not a
working server witness: they are removed from native admission. This is a limit
of the reviewed evidence, not proof that another server path cannot exist.
The exact per-method observations and finite discussion/invite/archive
coverage are recorded in `eitaa-inventory.json`. The actual `sendContact` /
`sendOther` path invokes `messages.sendMedia` with contact or geographic media;
native static-location and phone/name-contact sends use that same finite wire
path and persisted request ID. Live-location sharing and arbitrary vCards are excluded.
No full-schema support is claimed.

The official Android APK was inspected only for constructor IDs and field
layouts; no Android implementation was copied, translated or redistributed.
Its current `InputGeoPoint` layout is constructor `0xf3b7acc9`, latitude and
longitude as doubles, without flags. In a controlled official-Web request with
the catalog's `0x48222faf` form, nonzero synthetic coordinates returned as zero
and the latitude's raw double bits appeared as `updateMessageID.random_id`.
Zero-only coordinates had hidden that alignment failure. The native codec uses
the Android layout as its single current input form, with independent literal
tests through the final send nonce; the pinned MIT schema file remains intact.
Neither the incorrect acknowledgement nor matching content resolves a previous
unknown send.

Official-Web video observations distinguished a short silent MP4, which became
a filename-only document, from a five-second MP4 with audio, whose stored
document retained video attributes and a thumbnail. The reviewed upload path
sets streaming support and uploads an actual thumbnail with the original
video's `totalFileSize`, not the thumbnail byte count. Native preparation derives
that JPEG from a bounded first-frame AVC decoder in Go; it does not introduce a
runtime transcoder or fabricate media metadata. The preview's derived upload ID
is encrypted in the upload-stage journal before the first provider write.
Follow-up native trials retained video attributes for two-second silent and
five-second silent/audio samples. One-second samples with and without audio
were projected as files; the official Web's one-second sample also lost its video
attributes. This is an observed presentation limit, not a universal duration rule.

Rubika Web establishes separate account and conversation state tokens:
`getChats` supplies the initial state and page cursor; `getChatsUpdates` returns
`new_state`, changed chats and deleted chats; `getMessagesUpdates` returns
`new_state` and `updated_messages`. `OldState` requires a visible gap and a new
baseline. No local timestamp is invented as a provider recovery cursor. Socket
updates alone do not advance those state tokens. Account/peer checkpoint changes
and their events commit through the gateway's batch callback. Initial attachment
does not claim that all previous messages were imported.

The 2026-10-10 follow-up reviewed Eitaa Web's account/channel update routing and
compared live RPC responses within its already authenticated Web client. A
`updateChannelTooLong` notification causes a separate channel difference request;
it is not itself `updates.differenceTooLong`. This matches the independent stream
model described in [Telegram's update documentation](https://core.telegram.org/api/updates),
used only as a semantic cross-check; Eitaa's inspected wire remains authoritative.
Known channel invalidations now produce bounded `chat.updated` metadata, while
actual expired states, unresolved peer bindings and unknown variants retain
distinct diagnostics. Internal checkpoint version 2 preserves version-1 mixed
gap flags as `legacy_unclassified`, without inferring or deleting historical loss.

Official Eitaa Web RPC and native reads both returned a stale five-row history
window for a controlled conversation, while `limit=50` and its actual ID/date
offset returned the expected window. The adapter uses that single bounded read
profile and constructs the public cursor from the rows delivered to the caller.
A subsequent controlled conversation exceeding fifty messages established that
`limit=100` still returned fifty, with a placeholder `count=1000`. The single
wire profile therefore requests fifty even for a larger public limit; a full
provider page keeps its continuation. A public limit is an upper bound, and the
provider count is not treated as proof of remaining or complete history.
This is a reviewed read behavior, not a fallback write format. Official Web RPC
also rejected `messages.setTyping` with `INVALID_CONSTRUCTOR` and a fresh
`inputMediaPoll` request with `MEDIA_INVALID`, consistent with the native trials.

The inspected Rubika Web `getMessages` path treats `max_id` as inclusive and
subtracts one from its accepted minimum ID before requesting older messages.
Native history now derives its boundary from delivered rows even if the server
returns more than the requested limit. Initial `getMessagesInterval` responses
are accepted as bounded message windows with their peer state, instead of
discarding those messages. A retained expired-state flag remains visible but
does not prevent recovery of later messages; continued peer pages rotate so a
busy conversation cannot hold the front of the account queue indefinitely.

The controlled two-account outage test additionally established that the mutation
state is insufficient for new messages. The reviewed web client reads newer
messages with `getMessages`, `sort: FromMin` and `min_id`. The adapter now retains
a separate durable message-ID watermark, accepting each bounded page and its
watermark together before releasing the pending conversation. Inclusive duplicate
boundaries cannot rewind it; a non-advancing continued page is rejected. Existing
checkpoints without that watermark receive an explicitly marked bounded interval
baseline, not a claim of complete historical recovery. Sparse `Edit` updates can
contain only text and `is_edited`; they are retained as partial events without
inventing the original author, direction, creation date or attachment.
After an expired mutation state is replaced, its latest interval must not
overwrite an existing new-message watermark. The preserved watermark allows
continued `FromMin` pages to resume across a process restart. This recovers
observable new messages while retaining the explicit loss warning for edits or
deletions that an expired state can no longer prove.

The same Web bundle shows `rnd` on forwarding as well as normal sends,
`getPollOptionVoters` (correcting an old reference's spelling), and avatar
`main`/`thumbnail` references. Native downloads freshly resolve the selected
account's avatar and inspect image bytes. Provider update timestamps are retained
as provenance; their units are not guessed for edited/deleted event identities.
The reviewed Rubika reducer uses `update.timestamp` as UI `trackBy`; it does not
establish a monotonic ordering comparison. Conflicting attachment edits without
proven revision order therefore withhold download availability rather than expose
stale bytes. Rubika chat state also exposes `last_seen_my_mid` and
`last_seen_peer_mid`, which the Web UI compares to message IDs. Native chat-state
projection currently retains only the first field; dedicated receipt events,
per-message reader coverage and read timestamps are not claimed.
Eitaa uses actual per-update PTS, privately scoped to the account or
channel, for attachment replacement and deletion. Bare history/diff messages
never inherit the PTS of their containing page.

## Implemented evidence and reproducibility

Native Eitaa tests include literal TL values, parser bounds, password/auth
responses, durable token replacement, account/channel recovery, method contracts,
media upload transactions, poll closure, structured poll/contact/location updates, complete
permission replacement, cursor scope, discussion/invite administration and avatar
inspection. Scoped delete/read/content state projections preserve actual PTS where
present; peerless delete/content updates are explicit unresolved events. Neither
provider dates nor a peer binding is invented. Rubika tests include
an independent OpenSSL AES fixture, duplicate-key/depth/size guards,
password-before-OTP, API6 signing, detached sockets, account/peer checkpoint scope,
ambiguous writes, media stage journaling and bounded transfers, avatars and
finite communication operation fixtures. Both packages pass synthetic normal and
race tests. The JSON inventories record implementation separately from public
schema declarations and library convenience wrappers; unsupported variants retain
specific reasons instead of being counted as capabilities.

The opt-in native Rubika fixture is reproducible without accounts or credentials:

```sh
cd src
GOOMNI_MIXED_CAPACITY=1 go test -race ./internal/rubikameow \
  -run '^TestRunConcurrent100NativeClients$' -count=1 -v
go test ./internal/rubikameow -run '^$' \
  -fuzz '^FuzzEncryptedResponse$' -fuzztime=20s
go test ./internal/eitaameow -run '^$' \
  -fuzz '^FuzzTLDecoder$' -fuzztime=20s
```

The recorded native fixture used 100 distinct synthetic authentication values,
two connection cycles, 200 handshakes, 400 RPCs, 200 socket message updates and
200 checkpoint commits with at most four shared permits. Request cancellation
after connection did not end the connection's owned lifetime. This short local
fixture proves neither 100 real accounts nor sustained production capacity.
Operator-local logs live under ignored `artifacts/provider-fixtures/`; the combined
capacity runner records its own exact binary and complete verdict separately.

Original MIT copyright notices for every reused/reference project are preserved
in [THIRD_PARTY_NOTICES.md](../../THIRD_PARTY_NOTICES.md), in addition to the source
schema and Rubingo notice files. No GPL implementation was ported.

The same Eitaa worker declares `eitta_error` (constructor -404, integer code and
TL string text), which is absent from the MIT catalog. The codec recognizes
that reviewed layout and maps only known diagnostics, without exposing raw text.

Rubika discovery currently returns `default_sockets` as a bounded array of
WebSocket URLs. `NOT_REGISTERED` requests device registration rather than account
reauthentication; the native client registers its already-persisted signing
identity and checks the profile again. Service (`s0`) and bot (`b0`) conversations
are distinct from user/group/channel peers. They now retain their own `bot`/`service` peer types through text/media/reply,
edit/delete/forward, history/search, durable updates and filters. The official
bundle explicitly dispatches `getBotInfo(bot_guid)`,
`getServiceInfo(service_guid)`, `deleteBotChat(bot_guid,last_deleted_message_id)`
and `deleteServiceChat(service_guid,last_deleted_message_id)`. Those finite
conversation calls are independent implementations, not a Bot API or financial
integration. Provider permissions still apply; operation admission does not
assert every service permits writing.

Only provider `OldState` establishes an expired account/conversation recovery
state. Unsupported namespaces/content remain separate coverage diagnostics and
never by themselves mark recovery gap-detected. Checkpoint format 2 carries gap
provenance. Existing format-1 flags are preserved as `legacy_unclassified` since
their causes cannot be reconstructed. Upgrade discovery enumerates previously
skipped peers without replacing the saved account state with a newer listing
snapshot. No full initial history import is implied.

A controlled native Eitaa authorization response on 2026-10-10 carried a terminal
TL bytes field under authorization flag 10. The reference catalogs omit it.
The codec consumes this exact optional field and still rejects extra/truncated
bytes. Its semantics are unreviewed; it is never used as a token or public data.
The official worker treats `INVALID_LOGIN` as a bounded token-renewal request.
Native renewal persists replacements before activation and never repeats an
uncertain mutation.
