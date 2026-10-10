# Consumer integration contract

This guide describes the GoOmni 2.3.0 contract. Upgrade the gateway and consuming
backend together using the [GoBale upgrade guide](upgrade-goomni.md).
Update consumers with the gateway: account name uses `push_name`, media text uses
`caption`, multipart requests use ordinary fields and endpoint-named file parts,
and acknowledged operation IDs appear directly at `results.message_id`.
New message/edit events put their display-ready projection in `payload` and
reviewed native content in `content`; receipt fields expose distinct provider
timestamps. Previously persisted signed webhook bodies retain their original
bytes and format for retry/replay, including older events without `instance_id`.
Use documentation and OpenAPI from the installed release tag; the hosted API
reference follows `main` and can advance beyond a release.

GoOmni supplies messenger account lifecycle, durable provider operations and signed
events. The consuming application's backend owns users, organizations, channel
permissions, contacts, conversations, AI generation, approvals and billing. Its
browser must never receive GoOmni's machine credential. The administrative UI and
Basic credential grant gateway-wide administration; neither is a tenant login.

Any application can integrate through the same public API. Keep its user and
channel models in the consuming application; GoOmni does not depend on them.
[OpenAPI](openapi.yaml) is the HTTP contract; [webhook payloads](webhook-payload.md)
describe event content and delivery behavior.

Send machine API requests from a trusted backend over TLS when remote. Include
`APP_BASE_PATH` in every route if configured. Device listing and provisioning are
gateway-wide administration; never expose their results directly as a user's
authorized account list.

## Discover messenger availability

Call `GET /app/providers` before provisioning. Each descriptor reports `id`,
`name`, `enabled`, `verification` and `delivery_methods`. Only enabled providers
can create connections. `GET /app/capabilities?provider=bale` requires an explicit
provider; `GET /devices/{id}/capabilities` uses the guarded connection selection.
An unsupported operation is rejected before durable acceptance. New providers do
not inherit Bale's verified features, ID formats or recovery guarantees.

## Use one message integration across providers

Account provisioning, OTP/password stages, upload, ordinary send, operation
polling, scheduling, signed delivery and authenticated download use the same
contract for Bale, Eitaa and Rubika. Keep provider logic inside the gateway
client/adapter boundary; contacts, conversations, operators and application
permissions need not be restructured for each provider.

Treat all IDs as strings. Route a conversation by `(gateway, instance_id,
provider, peer.type, peer.id)`, then match a message within that scope by its
provider message ID. Message IDs can differ between sender and recipient accounts
(notably Eitaa); a reply/edit uses the ID observed by its selected connection.
Never correlate two accounts by assuming their message IDs are identical. Newly projected `payload.chat_id` equals `peer.id` across
providers; it is not a globally unique channel key. Never strip Eitaa's
`channel_` prefix or turn a Rubika GUID into a phone number.

For complete message/edit events, `payload.partial` is false and `payload.id`
matches envelope `message_id`. Edits also carry `original_message_id` with that
same value. Rubika may instead deliver `partial:true` edits: apply only present
reviewed fields to that scoped target. In particular, absent `body` preserves
text, whereas an explicit empty `body` clears it. A partial edit has no invented
sender or original timestamp; it must not become a new incoming message or an
operator takeover signal. See [patch examples](webhook-payload.md#routing-and-filters).
Historical signed delivery bodies keep their exact accepted contract.

`GET /app/providers` also returns `send` limits and `interactions`. Discover
optional behavior instead of assuming every messenger implements Bale presence
or GOWA acknowledgements:

| Behavior | Bale | Eitaa | Rubika |
| --- | --- | --- | --- |
| Start typing | `presence.typing` | Unavailable on tested server | `chat.activity`, `Typing` |
| Stop typing | `presence.stop` | Unavailable on tested server | Unavailable |
| Online/offline | `presence.online` | Unavailable | Unavailable |
| Mark read argument | `date` | `message_id` | `message_id` |
| Received receipt model | Timestamp watermark | Message-ID watermark | Chat state only |
| Delivered event | Available | Unavailable | Unavailable |
| Sender display name | Bounded enrichment, may be unavailable | Unavailable | Unavailable |
| Authenticated avatar download | Available | Available | Available |

Each interaction preset supplies a public `operation` and fixed `parameters`.
Look up that operation's method, path and mode in the provider catalogue, merge
its parameters with the selected `peer` if required, and call it with the usual
immutable instance guard. Mutation mode requires its own persisted idempotency
key; ephemeral mode follows the advertised contract. Missing presets mean skip
that optional feature; do not issue a guessed cancel/online request. Capability
availability describes implemented semantics, not completed live acceptance.
The reviewed Eitaa `chat.activity` operation remains in the source catalogue, but
controlled typing/cancel requests were rejected with `FEATURE_NOT_SUPPORTED`;
no active Eitaa typing preset is advertised. Consumers should skip it.

Read marking and received receipts are separate capabilities. Cumulative
watermarks do not prove an exact set of message IDs or their boundary inclusion.
Rubika `chat.updated.last_seen_my_mid` remains native chat state; do not synthesize
a shared delivered/read acknowledgement from it. Neither a receipt nor a typing
response can reconcile an unknown send. Render an absent sender name with an
application fallback and use the separate authenticated avatar endpoint rather
than relying on private provider media URLs.

Use `payload.media.type`, MIME and `download_supported` to handle media. Upload
and download are account scoped. Keep text in `message` for text sends, `caption`
for media; replies use `reply_message_id` with the selected peer. Voice must
already be valid Ogg Opus; the gateway does not transcode. Eitaa's tested two- and
five-second AVC MP4s retained video presentation and byte-identical downloads.
One-second samples arrived as files; follow the received media type rather than
assuming presentation from the send endpoint. Image sends may be
processed by the provider: the controlled Eitaa PNG arrived as a same-dimension
JPEG with a maximum one-level RGB difference. Use file sends when preserving
original image bytes is required; do not promise byte identity for photo sends. Runtime media notes
and the acceptance ledger distinguish format validation from provider rendering.
Do not split long text inside the gateway; respect the advertised byte/character
limit and persist each consumer-created part before submission.

## Persist one immutable reference per connected account

Store the mapping `(consumer tenant, channel) -> (gateway, device alias,
instance_id, provider, account_id)` in the consumer's database. One tenant may own several
channels/accounts. Authorize that mapping before every request and webhook
dispatch. The provider's `account_id` becomes available after authentication; it
is not a tenant identifier or a replacement for `instance_id`.

1. Persist a unique provisioning key and the exact initial configuration before
   `POST /devices`. Send `Idempotency-Key` and a body such as
   `{"device_id":"channel-42","provider":"bale","webhook_url":"https://consumer.example/bale/events","webhook_secret":"RANDOM_BACKEND_SECRET","webhook_events":["message","message.edited"]}`.
   The device and initial webhook configuration commit together. New creation
   returns 201; replay of the same key/configuration returns 200 and the same
   immutable device lifetime. Changed configuration under the key returns 409.
   The alias must be 1–64 ASCII letters, digits, dots, underscores or hyphens,
   starting with a letter or digit. A webhook URL requires a secret; unknown,
   duplicate and null provisioning fields are rejected.
2. Save the returned `id`, `instance_id` and immutable `provider`. Provisioning keys are global to this
   gateway database and separate from send keys. Replaying a key after deletion
   returns `PROVISIONING_RETIRED`; a deliberate replacement needs a new key.
   Replay never restores an old webhook configuration over later edits.
3. On every account-scoped read or write, send `X-Device-Id: <alias>` and
   `X-Device-Instance: <saved instance_id>`. A path or `device_id` query may select
   the alias, but all supplied selectors must agree. There is no HTTP fallback
   to a sole account. Missing selectors/guards return 400; a replaced instance
   returns 409. Conflicting selectors return 400; an unknown alias returns 404.
   Do not silently refresh a stale reference and repeat its write. The instance
   is an identity guard, not an authorization token.
4. Use `POST /devices/{id}/login` with `phone`, then submit `challenge_id` and
   `code` to `POST /devices/{id}/login/code`. Submit `challenge_id` and `password`
   to `POST /devices/{id}/login/password` only when the
   state is `awaiting_password`. `GET /devices/{id}/status` returns public
   challenge metadata together with auth, transport and recovery state. Respect expiry and resend
   cooldown independently. Public code delivery uses `delivery`, optional
   `next_delivery` and `available_deliveries`, not provider wire enum numbers; keep OTPs/passwords out of browser storage and logs.
5. Read `GET /devices/{id}/status` after login and persist its `account_id`.
   The same response includes `device_id` (alias), `instance_id`, `provider`, `auth`,
   `transport`, `recovery`, nullable `challenge` and `server_time`. It is a local
   snapshot with no provider RPC or implicit reconnect. `account_id` is empty
   before the first login; logout retains the account binding. Expired challenges
   are null and the auth state becomes `auth_required`. A bound device cannot
   change accounts. Keep authentication, transport and recovery states separate:
   `connected` does not mean the inbox has recovered every event.

Choose webhook destinations in trusted backend configuration. An authenticated
administrator can configure HTTP endpoints, including private destinations;
untrusted end users must not gain arbitrary callback or gateway URL control.

## Preserve messenger peer identities

Route with the returned `{type,id}` pair and the selected immutable connection.
Eitaa classic group `{"type":"group","id":"91"}` and supergroup
`{"type":"group","id":"channel_91"}` are different conversations; a broadcast channel
is `{"type":"channel","id":"91"}`. Keep the supergroup prefix in sends, history,
media references and webhook filters. Eitaa sender IDs remain numeric strings.
Do not strip prefixes, infer peer type from a number or reuse peer metadata from
another connection.

## Persist submissions and interpret their outcome

Before a send or scheduled submission, freeze its content and persist an
idempotency key together with the consumer's message/part record. Supply that key
to `/send/{kind}`, `/send/schedules` or a durable mutation endpoint. Keys are
required and scoped to the immutable connection; immediate sends and schedules
share a namespace. Reusing a key with changed content returns 409. Lifecycle
requests and media uploads do not use this durable-send namespace.

Use an opaque nonblank key of at most 256 bytes. Provisioning has its own
gateway-wide key namespace; send, schedule and durable mutation keys share the
selected connection's namespace. Changing a schedule or send into a different
request requires a deliberate new submission, not a new key generated by a
generic HTTP retry loop.

Store `send_id`, the request's `request_id`, and the eventual result beside the
consumer record. Acknowledged sends expose `results.message_id` directly; there
is no nested `results.result`. Always inspect `results.state`; `status` is a
display description. A successful HTTP response contains the REST
`code/message/results` envelope. Immediate writes can return:

| Result | Consumer action |
| --- | --- |
| 200, operation `succeeded` | Record the provider result; this does not prove recipient delivery/read. |
| 202, `queued` or `sending` | Poll `GET /send/operations/{send_id}` with the same immutable reference. |
| 202, `unknown` / `SEND_UNKNOWN` | Preserve uncertainty. Inspect/reconcile; never generate a new key to resend. |
| Failed operation with `results.send_id` | Retain the durable operation and diagnostic, including on a non-2xx response. |
| Transport timeout or lost response | Keep the original key/content. No blanket mutation retry; explicitly recover through the same keyed submission or a known operation ID. |

Compound native operations may also expose `stages`, ordered by `number`, with
`name`, `state`, `started_at` and `updated_at`. A stage is persisted before its
provider call. An upload stage succeeding does not mean its final message was
sent; inspect the enclosing operation and the final send stage. Private upload
references and provider nonces remain encrypted and are absent from the API.
Partial or ambiguous compounds remain `unknown` and are never automatically
replayed. The same operation ID and idempotency key retain their audit history.

An exact keyed resubmission returns the existing operation instead of repeating
its provider write. A previously accepted `unknown` operation remains uncertain.
Only reviewed provider evidence tied to account, peer and the persisted request
ID can resolve it. Similar text, an unverified acceptance event, or an own-message
notification by itself is insufficient. Keep earlier accepted parts if a later
part of a consumer message fails. Schedules return a schedule ID; inspect its
occurrences and associated operations for individual outcomes.

`GET /send/operations/{send_id}` returns 200 when the read succeeds even if the
operation is `unknown` or `failed`; always inspect `results.state`. Schedule
creation through `/send/{kind}` returns `status`, `schedule_id`, `scheduled_at`
and `next_run_at`; `/send/schedules` returns the full schedule record. Neither
proves that a send has happened.
Preserve the gateway-assigned `request_id` from the operation; do not choose or
replace it in a submission.

Bound consumer concurrency, outstanding work and polling. The gateway has global
and per-connection admission limits. `429 QUEUE_FULL` or
`CONNECTION_QUEUE_FULL` means new work was not admitted; back off rather than
starting an unbounded retry loop. Queued, sending and unknown operations count
toward these limits; unknown work is not discarded to free space. Existing keyed
operations remain inspectable at capacity. Creating a schedule does not reserve
outbox capacity for its future occurrences: a due occurrence waits if admission
is full. These are resource controls, not tenant authorization: the machine
credential can still administer every connection.

## Authenticate and commit incoming events

Select a webhook secret from trusted endpoint/channel configuration. Bound the
body and read time, then verify `X-Hub-Signature-256` as HMAC-SHA256 over the exact
raw bytes using a constant-time comparison. Do not parse and re-serialize before
checking. Reject malformed JSON and ambiguous identity fields.

New event envelopes carry `session_id` (alias), `instance_id` (immutable lifetime)
and `device_id` (provider account ID), plus `provider` on newly accepted events.
Check the immutable connection, alias and account against the saved mapping after
signature verification; never choose a tenant from an untrusted body alone.
The gateway overwrites provider-supplied identity with its stored connection.
Require `X-GoOmni-Event-Id` to match the body's `event_id`. Deduplicate by
`(gateway, instance_id, event_id)`, not the delivery ID. Commit the inbox record
before returning 2xx and process business effects from durable work afterward.
If persistence fails, return 503 so delivery can retry. HTTP 4xx other than
408/425/429 is permanent: the delivery fails and later events may proceed.
Use 422 for a permanently unprocessable event, or durably quarantine and return
2xx. Fix authentication/binding failures before explicitly retrying or replaying.
If an event arrives before the post-login account binding is committed, defer
acceptance until that trusted binding is available instead of learning it from
the incoming event.

Old persisted event/delivery bodies may lack `instance_id`; their signed bytes
are preserved. A strict new consumer must quarantine these or resolve them through
an explicitly reviewed historical mapping. Never attach a historical alias-only
event to the currently active alias automatically. Retries and replay retain
event identity; delivery IDs can change for administrative retry/replay.

Keep receipts monotonic and apply edit/delete/state events idempotently. Match
outgoing events by validated provider/request identity, not text similarity.
An external own-message may inform the consumer's human/AI handoff policy, but
the gateway does not decide assignment, permissions or whether AI should answer.

The tested [Go receiver example](../src/examples/webhookreceiver/main.go)
implements one fixed account binding with unchanged inbox-schema compatibility.
See [its setup instructions](webhook-payload.md#minimal-durable-receiver) for the
four required environment variables and a source-checkout command. It rejects
historical bodies without an instance and preserves the first body on duplicate
event IDs. It supplies no tenant routing or business worker; review existing
inbox rows before processing an old database.

Use the delivery ledger for recovery after a failed callback. Automatic retries
keep the delivery ID; administrative retry creates a replacement for the same
destination, and replay selects current routing/filter rules. Replay can create
another delivery even for an already successful event. These administrative
actions have no idempotency-key contract; inspect the ledger after a lost response
before repeating them. See [retry and replay](webhook-payload.md#retries-changes-and-replay).

## Use the Bale account contract

| Topic | GoOmni contract |
| --- | --- |
| Authentication | Native phone/OTP flow, optional password challenge, immutable device instance. |
| Peer identity | Structured `peer: {type,id}` with decimal string IDs. Do not infer phone numbers from peer IDs. |
| Message events | `payload` contains display-ready `body`, sender name, identity, reply/edit/forward and media fields; `content` retains reviewed native structured content. Unknown sender/direction remains explicit. |
| Receipts | Preserve `start_date` plus `read_date`/`received_date`; own-read has separate optional `end_date`. These are provider timestamps, not two validated range endpoints. Zero remains zero; exact per-message coverage is unverified and `message_ids_supported` is false. See [receipt timestamps](webhook-payload.md#receipt-timestamps). |
| Send outcomes | Durable operation ID/state, persisted request ID, and eventual provider result; 202 is not completed delivery. |
| Downloads | Authenticated, connection-scoped binary download through GoOmni; no provider URL/hash credentials in public JSON. |
| Formatting and limits | Validate the selected Bale operation's documented formatting, size and editing contract. |

Keep public IDs as strings, including negative signed message/file IDs. Use
`message_id` to identify a message within its peer/connection; `event_id`
identifies an event and is not interchangeable with it. Use the documented event
names instead of translating every unknown event into an empty successful text
message. Unsupported variants and recovery gaps must remain visible.

For incoming events, the gateway resolves sender names before durable acceptance.
A sender absent from contacts can be found through a bounded scan of recent
conversations; reads use only references established by the selected account.
Cold senders wait for their per-connection lookup slot, including when two new
senders arrive close together. A lookup can add up to one second of rate-limit
wait and 500 ms of provider reads; preceding queued events add further delay.
Queue limits and recovery status still apply. See [sender-name lookup and
limits](webhook-payload.md#received-content).

Render `payload.sender_display_name` when `sender_name_status` is `available`, and
handle `unavailable` explicitly. Names are display labels, not identity or access
proof. Provider failure, unavailable/unsafe names and lookup bounds can still
prevent enrichment. Consumers need no separate profile request to activate the
lookup. Completed unavailable lookups are cached for 30 seconds; a later cache
fill does not change previously committed messages or generate a completion
event. Retry/replay preserves the original signed body.

For attachments, honor `download_supported` and use
`GET /message/{message_id}/download?peer=type:id` with the same saved device
reference. Local uploaded media use `/media/{media_id}`. Stream with a deadline
and maximum byte count, close bodies on every exit, and never forward machine
credentials to redirects or provider-supplied URLs. Inspect content before storing
or serving it. Voice sends require validated Ogg Opus through `/send/voice`;
caller-declared duration or a media-type flag is insufficient. Avatars use the
separate authenticated `/user/avatar` route.

## Send a file in one request

`POST /send/file`, `/send/image`, `/send/video`, `/send/audio` and `/send/voice`
also accept `multipart/form-data` with ordinary fields and one endpoint-named
file part: `file`, `image`, `video` or `audio` (`/send/voice` also uses `audio`).
Choose `phone` or a `peer` JSON object. Use `caption`, `reply_message_id` and
optional `mentions` as a JSON array of real Bale user IDs. Combined form metadata
is limited to 128 KiB, the filename to 255 UTF-8 bytes, all parts to 20 and the
file to the configured media limit. Omitted MIME defaults to
`application/octet-stream`. Keep the same account/instance headers and durable
`Idempotency-Key`. The former `request` JSON form part is no longer accepted.

```sh
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GATEWAY_DEVICE" \
  -H "X-Device-Instance: $GATEWAY_INSTANCE" \
  -H 'Idempotency-Key: attachment-42' \
  -F 'peer={"type":"user","id":"123"};type=application/json' \
  -F 'caption=Attachment' \
  -F 'image=@./picture.webp;type=image/webp' \
  "$GATEWAY_URL/send/image"
```

To schedule that same upload, include `scheduled_at` and an IANA `timezone`.
Optional recurrence fields use the JSON send names; `weekdays` is a JSON array
and `day_of_month`/`occurrence_limit` are integers. The response contains
`status:"Message scheduled"`, `schedule_id`, `scheduled_at` and `next_run_at`.
The file and retaining schedule commit together, before any provider contact.
The scheduler later creates each occurrence and its outbox entry atomically.

Immediate uploads likewise commit media registration and outbox acceptance
together. An identical keyed retry binds file bytes, filename, MIME and canonical
request content, including scheduling fields. It returns the original operation
or schedule after restart, including a completed schedule or uncertain send;
changed content returns 409. JSON sends and schedules share this key namespace,
so changing the submission form/content under a used key can conflict. Unused
retry/conflict staging files are cleaned. Use `/media` first when reusing an asset.

JSON media sends use `caption` too; `message` is only for text sends. Unknown
fields, duplicate form fields, unsupported URL/compression/view-once flags and
caller request IDs are rejected rather than silently ignored. Account display
name changes use `push_name` on `/user/name` and `/operations/account.name`;
`name` remains an internal persisted argument, not an HTTP input alias.

On `/send/audio`, the optional form field `ptt=true` selects native voice validation;
`ptt=false` sends ordinary audio. This flag does not convert the file: voice still
requires complete valid Ogg Opus and gateway-derived duration. `/send/voice`
selects the same voice path directly. Image sends accept bounded WebP decoding
alongside JPEG/PNG/GIF, with at most 8192 pixels per side and 16,777,216 pixels
total; native WebP receipt/rendering remains live-unverified.

## Verification boundary

`src/ui/rest/saas_flow_test.go` exercises three synthetic connections, with two
named for one conceptual consumer and one for another, against GoOmni's real
REST/storage/usecase stack. It checks immutable selection, separate OTP/password
state, durable send keys/unknown outcomes, account-scoped resources and a signed
webhook retry across SQLite close/reopen. It does not prove consumer tenant ACLs
or application integration, contact Bale, or establish native-account capacity.
The consumer must test its own authorization and transactional inbox/outbox
against these contracts in its complete application.
