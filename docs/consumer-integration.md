# Consumer integration contract

The consumer message projection, multipart sends, receipt validity fields,
combined status and permanent webhook-failure policy below were introduced in
2.1.0; use a matching gateway build and contract. The sender-name lookup fixes
described below are in [Unreleased](../CHANGELOG.md#unreleased), after 2.1.0.

This guide describes the GoBale 2.0 API contract. Atomic keyed provisioning,
mandatory machine-API instance guards, required send/schedule keys and new-event
`instance_id` were introduced in [2.0.1](../CHANGELOG.md#201--2026-10-07).
Consumers upgrading from 1.x must adopt these rules with the gateway upgrade.
Use the documentation and OpenAPI from the same tag as your installed release;
the hosted API reference follows `main` and can advance beyond a release.

GoBale supplies Bale account lifecycle, durable provider operations and signed
events. The consuming application's backend owns users, organizations, channel
permissions, contacts, conversations, AI generation, approvals and billing. Its
browser must never receive GoBale's machine credential. The administrative UI and
Basic credential grant gateway-wide administration; neither is a tenant login.

Any application can integrate through the same public API. Keep its user and
channel models in the consuming application; GoBale does not depend on them.
[OpenAPI](openapi.yaml) is the HTTP contract; [webhook payloads](webhook-payload.md)
describe event content and delivery behavior.

Send machine API requests from a trusted backend over TLS when remote. Include
`APP_BASE_PATH` in every route if configured. Device listing and provisioning are
gateway-wide administration; never expose their results directly as a user's
authorized account list.

## Persist one immutable reference per connected account

Store the mapping `(consumer tenant, channel) -> (gateway, device alias,
instance_id, account_id)` in the consumer's database. One tenant may own several
channels/accounts. Authorize that mapping before every request and webhook
dispatch. The provider's `account_id` becomes available after authentication; it
is not a tenant identifier or a replacement for `instance_id`.

1. Persist a unique provisioning key and the exact initial configuration before
   `POST /devices`. Send `Idempotency-Key` and a body such as
   `{"device_id":"channel-42","webhook_url":"https://consumer.example/bale/events","webhook_secret":"RANDOM_BACKEND_SECRET","webhook_events":["message","message.edited"]}`.
   The device and initial webhook configuration commit together. New creation
   returns 201; replay of the same key/configuration returns 200 and the same
   immutable device lifetime. Changed configuration under the key returns 409.
   The alias must be 1–64 ASCII letters, digits, dots, underscores or hyphens,
   starting with a letter or digit. A webhook URL requires a secret; unknown,
   duplicate and null provisioning fields are rejected.
2. Save the returned `id` and `instance_id`. Provisioning keys are global to this
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
   cooldown independently; keep OTPs/passwords out of browser storage and logs.
5. Read `GET /devices/{id}/status` after login and persist its `account_id`.
   The same response includes `device_id` (alias), `instance_id`, `auth`,
   `transport`, `recovery`, nullable `challenge` and `server_time`. It is a local
   snapshot with no provider RPC or implicit reconnect. `account_id` is empty
   before the first login; logout retains the account binding. Expired challenges
   are null and the auth state becomes `auth_required`. A bound device cannot
   change accounts. Keep authentication, transport and recovery states separate:
   `connected` does not mean the inbox has recovered every event.

Choose webhook destinations in trusted backend configuration. An authenticated
administrator can configure HTTP endpoints, including private destinations;
untrusted end users must not gain arbitrary callback or gateway URL control.

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
consumer record. A successful HTTP response contains the REST
`code/message/results` envelope. Immediate writes can return:

| Result | Consumer action |
| --- | --- |
| 200, operation `succeeded` | Record the provider result; this does not prove recipient delivery/read. |
| 202, `queued` or `sending` | Poll `GET /send/operations/{send_id}` with the same immutable reference. |
| 202, `unknown` / `SEND_UNKNOWN` | Preserve uncertainty. Inspect/reconcile; never generate a new key to resend. |
| Failed operation with `results.send_id` | Retain the durable operation and diagnostic, including on a non-2xx response. |
| Transport timeout or lost response | Keep the original key/content. No blanket mutation retry; explicitly recover through the same keyed submission or a known operation ID. |

An exact keyed resubmission returns the existing operation instead of repeating
its provider write. A previously accepted `unknown` operation remains uncertain.
Only reviewed provider evidence tied to account, peer and the persisted request
ID can resolve it. Similar text, an unverified acceptance event, or an own-message
notification by itself is insufficient. Keep earlier accepted parts if a later
part of a consumer message fails. Schedules return a schedule ID; inspect its
occurrences and associated operations for individual outcomes.

`GET /send/operations/{send_id}` returns 200 when the read succeeds even if the
operation is `unknown` or `failed`; always inspect `results.state`. Schedule
creation likewise returns a schedule record, not proof that a send has happened.
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
and `device_id` (Bale account ID). Check all three against the saved mapping after
signature verification; never choose a tenant from an untrusted body alone.
The gateway overwrites provider-supplied identity with its stored connection.
Require `X-GoBale-Event-Id` to match the body's `event_id`. Deduplicate by
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

| Topic | GoBale contract |
| --- | --- |
| Authentication | Native phone/OTP flow, optional password challenge, immutable device instance. |
| Peer identity | Structured `peer: {type,id}` with decimal string IDs. Do not infer phone numbers from peer IDs. |
| Message events | `message` contains display-ready `body`, sender name, identity, reply/edit/forward and media fields; `payload` retains reviewed structured content. Unknown sender/direction remains explicit. |
| Receipts | `range_status` marks positive ordered bounds as `valid`, zero/missing bounds as `unknown`, and reversed bounds as `invalid`. This is structural validation, not per-message proof; `message_ids_supported` is false. |
| Send outcomes | Durable operation ID/state, persisted request ID, and eventual provider result; 202 is not completed delivery. |
| Downloads | Authenticated, connection-scoped binary download through GoBale; no provider URL/hash credentials in public JSON. |
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

Render `message.sender_display_name` when `sender_name_status` is `available`, and
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
also accept `multipart/form-data`: exactly one `request` JSON field and one
`file` part with its filename and actual MIME type. The request field is limited
to 128 KiB, the filename to 255 bytes and the file to the configured media limit.
Omitting the file MIME defaults to `application/octet-stream`. Keep the same account and
instance headers and a persisted `Idempotency-Key`. For example:

```sh
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GOBALE_DEVICE" \
  -H "X-Device-Instance: $GOBALE_INSTANCE" \
  -H 'Idempotency-Key: attachment-42' \
  -F 'request={"peer":{"type":"user","id":"123"},"message":"Attachment"}' \
  -F 'file=@./picture.webp;type=image/webp' \
  "$GOBALE_URL/send/image"
```

Upload registration and outbox acceptance commit together before provider contact.
An identical retry compares file bytes, filename, MIME and request content and
returns the same operation, including after restart. Keep all of these stable;
changed content returns 409. The key namespace is shared with JSON sends and
schedules. Unused staging files are cleaned after errors and duplicate retries.
Multipart requests are immediate; upload through `/media` first for scheduled or
reusable attachments. Poll operation state as for JSON sends.

On `/send/audio`, the optional form field `ptt=true` selects native voice validation;
`ptt=false` sends ordinary audio. This flag does not convert the file: voice still
requires complete valid Ogg Opus and gateway-derived duration. `/send/voice`
selects the same voice path directly. Image sends accept bounded WebP decoding
alongside JPEG/PNG/GIF, with at most 8192 pixels per side and 16,777,216 pixels
total; native WebP receipt/rendering remains live-unverified.

## Verification boundary

`src/ui/rest/saas_flow_test.go` exercises three synthetic connections, with two
named for one conceptual consumer and one for another, against GoBale's real
REST/storage/usecase stack. It checks immutable selection, separate OTP/password
state, durable send keys/unknown outcomes, account-scoped resources and a signed
webhook retry across SQLite close/reopen. It does not prove consumer tenant ACLs
or application integration, contact Bale, or establish native-account capacity.
The consumer must test its own authorization and transactional inbox/outbox
against these contracts in its complete application.
