# Webhook payloads

This guide describes GoBale 2.0. Required machine-API instance guards,
`webhook_filter` and new-event `instance_id` were introduced in
[2.0.1](../CHANGELOG.md#201--2026-10-07). Use the documentation from the same tag
as your installed release; the hosted API reference follows `main`. Previously
stored webhook bodies retain their original bytes and may lack `instance_id`.

GoBale stores events before delivery and sends a signed JSON **event**, without
REST's `code/message/results` wrapper. Delivery is **at least once**: verify the
signature, persist the event and deduplicate its `event_id` before returning a
2xx response. Process business work from that durable inbox.

## Routing and filters

Configure a device through `PATCH /devices/{device_id}/webhook`. Set
`GOBALE_URL` to your gateway URL, including `APP_BASE_PATH` if configured;
`GOBALE_DEVICE` to its local alias; and `GOBALE_INSTANCE` to the saved
`instance_id` returned at creation or by `GET /devices`. Keep the instance in
your account mapping instead of automatically refreshing it after a conflict.
The alias in this route selects the account, so an additional `X-Device-Id`
header is optional; if supplied, it must match.

Use a privately stored random webhook secret in place of the sample placeholder:

```sh
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Instance: $GOBALE_INSTANCE" \
  -X PATCH -H 'Content-Type: application/json' \
  --data '{"webhook_url":"https://your-app.example/bale/events","webhook_secret":"REPLACE_WITH_A_RANDOM_SECRET","webhook_events":["message","message.edited","message.deleted"]}' \
  "$GOBALE_URL/devices/$GOBALE_DEVICE/webhook"
```

An enabled device URL requires a nonempty secret. This patch does not require
an `Idempotency-Key`; atomic initial provisioning through `POST /devices` does.
Use `GET /devices/{device_id}/webhook` with the same instance header to inspect
`routing_mode`, `routing_rules` and `secret_configured` without retrieving secrets.

The device URL overrides global `BALE_WEBHOOK` destinations by default. An empty
URL restores global fallback; `BALE_WEBHOOK_DEVICE_MERGE_GLOBAL=true` adds globals
alongside an override. Identical destination URLs receive one delivery. Global
destinations share `BALE_WEBHOOK_SECRET`; the device secret is separate and
write-only. Configuration reads never return secrets.

`webhook_events` applies to the device destination: names match exactly, `[]` or
`["*"]` accepts all events. Prefix patterns such as `"message.*"` are not
supported. Filtering an event from an existing override does not fall back to
globals unless merge mode is enabled. Without a device URL, its filter does not
filter global deliveries. Global settings are loaded at process startup.

`webhook_filter` adds peer, peer-type, sender and direction constraints:

```json
{
  "webhook_filter": {
    "peer_types": ["user"],
    "directions": ["incoming"],
    "exclude_sender_ids": ["123"]
  }
}
```

Supported fields are `peers`, `exclude_peers` (arrays of `{type,id}`),
`peer_types`, `sender_ids`, `exclude_sender_ids` and `directions`. Each list accepts
at most 100 unique values. Peer types are `user`, `group` and `channel`; peer and
sender IDs must be canonical positive uint32 strings, without leading zeros.
Directions are `incoming`, `outgoing` and `unknown`. Values within one list are
ORed; different fields are ANDed; exclusions win. Empty lists impose no restriction.
Missing peer/sender metadata cannot satisfy the corresponding include rule.
Direction filtering treats missing or invalid direction as `unknown`; a message
or edit without a valid sender also matches `"directions":["unknown"]`.

In a patch, omitting `webhook_filter` preserves it. Sending the object replaces
the **entire** filter, so omitted fields inside it lose their previous constraints.
Send `"webhook_filter":{}` to clear it; `"webhook_filter":null` is rejected.
The administrative webhook editor exposes the same rules.

Routing is evaluated inside the event-acceptance transaction. Rejecting delivery
does not discard the event or prevent its checkpoint from committing. Filter
changes affect new events and explicit replay; pending deliveries and their
stable event identities are unchanged. Global destinations retain existing merge
semantics. An absent sender is omitted and its direction is `unknown`; an edit's
updater is not guessed to be the original message author.

### Event names

Use these exact strings in `webhook_events`. The catalog describes decoded
variants, not a promise that every event has been observed live or that the
provider emits every variant for every account. Payload fields below are
representative and depend on the event; some notifications carry `{}`.

| Family | Exact event names | Representative payload fields |
| --- | --- | --- |
| Messages | `message`, `message.edited`, `message.deleted`, `message.accepted` | Normalized content; acceptance has `accepted:true`; deletion has `{}`. |
| Receipts | `message.read`, `message.received`, `message.read_by_me` | `start_date`, `date`; own-read may include `end_date`, `unread_count`. |
| Pins and chats | `message.pinned`, `message.unpinned`, `chat.cleared`, `chat.deleted` | Pins may carry `message_id`, `date`, `sender_id`, `content`; chat changes carry `{}`. |
| Users | `user.username_changed`, `user.about_changed`, `user.blocked`, `user.unblocked` | `username` or `about`; block changes carry `{}`. |
| Presence | `presence.typing`, `presence.typing_stopped`, `presence.online`, `presence.offline`, `presence.last_seen` | Typing: `user_id`, `typing_type`; status: `device_type`, optional `device_category`; last-seen adds `date`. |
| Reactions | `message.reactions_changed`, `message.new_reaction`, `message.reactions_read_by_me` | `reactions`, `reaction_by_me`, or the reaction position's `date`. |
| Group profile | `group.title_changed`, `group.username_changed`, `group.topic_changed`, `group.about_changed`, `group.avatar_changed`, `group.restriction_changed` | The corresponding `title`, `username`, `topic`, `about` or `restriction`; avatar change carries `{}`. |
| Group membership | `group.membership_changed`, `group.members_changed`, `group.members_count_changed` | `is_member`, a `members` list, or `members_count`. |
| Group rights | `group.owner_changed`, `group.admin_changed`, `group.member_permissions_changed`, `group.default_permissions_changed`, `group.can_send_changed`, `group.can_view_members_changed`, `group.can_invite_changed` | `user_id`, `is_admin`, `permissions`, `can_send_messages`, `can_view_members` or `can_invite_members`. |
| Group state | `group.members_became_async`, `group.history_shared`, `group.orphaned` | `{}`; identify the group from the envelope's peer. |
| Recovery and protocol | `connection.recovery`, `protocol.unsupported_update` | Recovery: `phase`, `gap_detected`; unsupported update: `supported:false`, sometimes `gap_detected:true`. |

For example, `group.membership_changed` can have `{"is_member":false}`;
`message.read` carries a provider date range rather than a message ID. Account
and recovery notifications may have no conversation peer, and their direction
can be `unknown`.

## Envelope and signature

Synthetic incoming-message example:

```json
{
  "event_id": "6ab408125819e603307557854273d663470da881d2cfbb71c68354ce1bc3f1fa9",
  "event": "message",
  "device_id": "123",
  "session_id": "support",
  "instance_id": "1111111111111111111111111111111111111111111111111111111111111111",
  "peer": {"type": "user", "id": "456"},
  "message_id": "987654321",
  "sender_id": "456",
  "direction": "incoming",
  "timestamp": "2026-10-06T12:00:00Z",
  "payload": {"kind": "text", "message": "Hello"}
}
```

| Field | Meaning |
| --- | --- |
| `event_id` | Stable, connection-scoped event identity; use it for durable deduplication. |
| `event` | Event name, such as `message` or `message.edited`. |
| `session_id` | Local device alias used by `X-Device-Id`, such as `support`. |
| `instance_id` | Immutable connection identity, matching the saved device instance; present on newly persisted events. |
| `device_id` | Connected Bale account ID, not the local alias. |
| `peer` | Conversation type/ID where applicable; account-level notifications may have empty peer fields. |
| `message_id`, `sender_id`, `direction` | Optional event-specific fields; message direction is `incoming`, `outgoing` or `unknown` when provenance is absent. |
| `timestamp` | RFC3339 event timestamp; no universal ordering guarantee is implied. |
| `payload` | Normalized event-specific content; not raw provider protobuf. |

Keep all IDs as strings. Message/file IDs may be negative signed 64-bit values;
they must not pass through a JavaScript `Number`. Do not parse meaning from
`event_id` or infer tenant permission from the device alias alone.

After verifying the raw-body signature, match `session_id`, `instance_id` and
`device_id` to the consumer's saved alias, instance and Bale account binding.
The gateway sets these fields from storage, never from untrusted provider metadata. A channel
with no authenticated account binding must defer business processing until it
has confirmed that binding through the authenticated device API. Return non-2xx
for a retry, or acknowledge only after a durable quarantine write; do not guess
ownership from the incoming event or message content.
Historical bodies created before `instance_id` was introduced are preserved on
retry/replay. Do not automatically bind such a body to a newly created channel;
handle it through the operator's existing connection record. See the
[consumer integration contract](consumer-integration.md).

Every delivery includes:

- `Content-Type: application/json`
- `X-Hub-Signature-256: sha256=<lowercase HMAC-SHA256 hex>`
- `X-GoBale-Event-Id` and `X-GoBale-Delivery-Id`
- `X-Webhook-Id` (the same event ID, for compatibility)

Compute the HMAC over the **exact body bytes**, using the selected destination's
secret. Compare in constant time before accepting parsed content. Parsing and
re-serializing JSON changes the bytes. Reject duplicate JSON fields and ambiguous
identity fields, and require `X-GoBale-Event-Id` to match the body's `event_id`.
Retries/replay preserve the event body and identity, but a new ledger entry can
have another delivery ID. Deduplicate by `(gateway, instance_id, event_id)` when
consuming more than one gateway/account. A secret rotation changes the signature,
not the persisted body.

## Received content

Message payloads use `kind` and optional normalized fields. Text is in `message`;
documents can include `name`, `mime_type`, `size`, `caption`, `media_type` and
`download_supported`. Native voice arrives as `kind:"document"`,
`media_type:"voice"`, with `duration` in milliseconds. Do not assume every media
variant uses the same duration unit.

Download a registered attachment through the authenticated
`GET /message/{message_id}/download?peer=user:ID` route using the same device.
Send both `X-Device-Id` and `X-Device-Instance` on this account-scoped route.
History can register an attachment too. Respect `download_supported`; a visible
file ID alone is not a download credential. Provider locations/hashes remain
private, and quoted content does not grant a new attachment reference.

Contact photos are fetched separately through authenticated
`GET /user/avatar?peer=user:ID&size=small|large`. Success returns image bytes, not
JSON or a public URL. The requested size may fall back to an available rendition;
`AVATAR_NOT_FOUND` means no photo visible to that account, including private or
absent photos. This user route does not imply group/channel-avatar support.

Payloads may include `quoted_message`, forward context, mentions, service actions,
polls, stickers, contacts, locations, gifts or nested template `content`. Render
nested content deliberately instead of assuming top-level text. Keyboard metadata
does not execute a URL, callback or Mini App, and receiving a template does not
prove ordinary-account template sending works. Anonymous poll voters and private
gift/financial fields are not exposed. Incoming gifts do not authorize payments.

Other events include edits/deletions, receipt ranges, pins, chat changes,
account/group metadata, membership/permissions, presence and reactions. Receipts
can describe ranges; do not fabricate individual-message receipts from them.
State transitions can legitimately repeat. Undated recovery pages may yield
duplicate state notifications; consumers should apply those states idempotently.
Unsupported variants remain observable, including `protocol.unsupported_update`.
An event family in the schema is not proof of every provider variant working live.

See [OpenAPI](openapi.yaml) for schemas and the [README](../readme.md) for current
support limits. Do not log message bodies or credential-bearing Mini App results.

## Retries, changes and replay

Each delivery has a 10-second request timeout and a normal budget of eight attempts,
with exponential backoff and equal jitter. Non-2xx responses, network errors and
timeouts retry; redirects are not followed. An interrupted attempt can repeat
after a crash or shutdown, so eight is not an absolute HTTP-request maximum.
Ordering is per connection/destination: a slow target blocks later events for
that target, while other destinations/accounts continue. Exhausted deliveries
remain inspectable rather than disappearing.

A 2xx response acknowledges durable receipt, not completion of the consumer's
business work. All non-2xx responses are retried, including 400/401/403; a
signature or binding error therefore needs a configuration fix, not just waiting.

Changing a device URL pauses unstarted old-configuration work. Secret-only rotation
applies on the next attempt for the same URL; an in-flight request may still use
the previous secret. After restart, removed global destinations pause and an
unchanged global URL uses its current secret. Original destination snapshots
remain in the delivery ledger.

Use the same saved device reference for inspection and administrative recovery.
First inspect the delivery; set `DELIVERY_ID` to its `delivery_id` from the list,
then choose either retry or replay as appropriate:

```sh
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GOBALE_DEVICE" -H "X-Device-Instance: $GOBALE_INSTANCE" \
  "$GOBALE_URL/deliveries?limit=20&include_payload=false"
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GOBALE_DEVICE" -H "X-Device-Instance: $GOBALE_INSTANCE" \
  "$GOBALE_URL/deliveries/$DELIVERY_ID"

# Retry only this unchanged destination:
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GOBALE_DEVICE" -H "X-Device-Instance: $GOBALE_INSTANCE" -X POST \
  "$GOBALE_URL/deliveries/$DELIVERY_ID/retry"

# Or explicitly replay to the targets accepted by the current routing rules:
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -H "X-Device-Id: $GOBALE_DEVICE" -H "X-Device-Instance: $GOBALE_INSTANCE" -X POST \
  "$GOBALE_URL/deliveries/$DELIVERY_ID/replay"
```

Retry replaces a `failed` or `retry` delivery for its unchanged destination with a
new ledger entry at the original durable queue position. A waiting later event
cannot overtake that retry; events already delivered cannot be reordered.
A changed destination requires explicit replay, which appends deliveries for
current targets and can redeliver to a previously successful target. Neither
changes the event ID. Inspect the outcome before replaying again.

Automatic retry keeps the delivery ID. Administrative `/retry` cancels its old
ledger row and creates a replacement; the HTTP response confirms the action but
does not return the new ID, so inspect `/deliveries` afterward. `/replay` returns
the new delivery rows and leaves the original row unchanged. It can return 409
`NO_WEBHOOK_TARGETS` if no current target accepts the event. `/retry` returns 409
`DELIVERY_CONFLICT` for an ineligible state or changed target.

These administrative actions have no idempotency-key contract. Repeating
`/replay` creates another delivery set even if you supply `Idempotency-Key`.
After a lost response, inspect the ledger before taking another action.

## Minimal durable receiver

The repository's [Go receiver](../src/examples/webhookreceiver/main.go) is a tested
example for **one fixed account binding**. It verifies the raw-body signature,
rejects duplicate/ambiguous identity fields, matches the event header and saved
binding, and commits an SQLite inbox before acknowledging. It accepts at most
4 MiB per body, has HTTP read/write deadlines, and returns 503 if storage fails.

Configure these environment variables privately:

| Variable | Value |
| --- | --- |
| `WEBHOOK_SECRET` | The secret configured for this delivery destination |
| `WEBHOOK_DEVICE_ID` | The saved local alias (`session_id` in events) |
| `WEBHOOK_INSTANCE_ID` | The saved device `instance_id` |
| `WEBHOOK_ACCOUNT_ID` | The authenticated Bale `account_id` (`device_id` in events) |

All four are required. From a source checkout matching the gateway contract,
with Go 1.26.6 installed, run:

```sh
cd src
go run -tags purego ./examples/webhookreceiver
```

The example listens on `http://127.0.0.1:8080/events` and stores
`webhook-inbox.db` in its working directory. That URL is reachable directly only
when the gateway shares the host/network namespace; a container needs a receiver
address reachable from its own network. Remote ingress needs an operator-managed
TLS endpoint. Protect the database and its WAL files: they contain event bodies.

A duplicate event preserves the first accepted raw body. Historical bodies
without `instance_id` are rejected. Existing inbox rows are not migrated or
re-authorized; review them before a worker processes an old database.

Run a separate worker over committed rows. Make business effects transactional
with processing state, or use a durable outbox. The example does not implement
multi-account routing, application authorization, retention or a business worker.
See the [consumer integration contract](consumer-integration.md) for those
application responsibilities.
