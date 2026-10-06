# Webhook payloads

GoBale stores events before delivery and sends a signed JSON **event**, without
REST's `code/message/results` wrapper. Delivery is **at least once**: verify the
signature, persist the event and deduplicate its `event_id` before returning a
2xx response. Process business work from that durable inbox.

## Routing and filters

Configure a device through `PATCH /devices/{device_id}/webhook`:

```sh
curl --fail-with-body --user "$APP_BASIC_AUTH" \
  -X PATCH -H 'Content-Type: application/json' \
  --data '{"webhook_url":"https://your-app.example/bale/events","webhook_secret":"REPLACE_WITH_A_RANDOM_SECRET","webhook_events":["message","message.edited","message.deleted"]}' \
  http://127.0.0.1:3000/devices/support/webhook
```

The device URL overrides global `BALE_WEBHOOK` destinations by default. An empty
URL restores global fallback; `BALE_WEBHOOK_DEVICE_MERGE_GLOBAL=true` adds globals
alongside an override. Identical destination URLs receive one delivery. Global
destinations share `BALE_WEBHOOK_SECRET`; the device secret is separate and
write-only. Configuration reads never return secrets.

`webhook_events` applies to the device destination: names match exactly, an empty
array or `"*"` accepts all events. Prefix patterns such as `"message.*"` are not
supported. Filtering an event from an existing override does not fall back to
globals unless merge mode is enabled. Without a device URL, its filter does not
filter global deliveries. Global settings are loaded at process startup.

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
| `device_id` | Connected Bale account ID, not the local alias. |
| `peer` | Conversation type/ID where applicable; account-level notifications may have empty peer fields. |
| `message_id`, `sender_id`, `direction` | Optional event-specific fields; message direction is `incoming` or `outgoing`. |
| `timestamp` | RFC3339 event timestamp; no universal ordering guarantee is implied. |
| `payload` | Normalized event-specific content; not raw provider protobuf. |

Keep all IDs as strings. Message/file IDs may be negative signed 64-bit values;
they must not pass through a JavaScript `Number`. Do not parse meaning from
`event_id` or infer tenant permission from the device alias alone.

Every delivery includes:

- `Content-Type: application/json`
- `X-Hub-Signature-256: sha256=<lowercase HMAC-SHA256 hex>`
- `X-GoBale-Event-Id` and `X-GoBale-Delivery-Id`
- `X-Webhook-Id` (the same event ID, for compatibility)

Compute the HMAC over the **exact body bytes**, using the selected destination's
secret. Compare in constant time before accepting parsed content. Parsing and
re-serializing JSON changes the bytes. Retries/replay preserve event identity,
but a new ledger entry can have another delivery ID; deduplicate by event ID.

## Received content

Message payloads use `kind` and optional normalized fields. Text is in `message`;
documents can include `name`, `mime_type`, `size`, `caption`, `media_type` and
`download_supported`. Native voice arrives as `kind:"document"`,
`media_type:"voice"`, with `duration` in milliseconds. Do not assume every media
variant uses the same duration unit.

Download a registered attachment through the authenticated
`GET /message/{message_id}/download?peer=user:ID` route using the same device.
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

Each destination has a 10-second timeout and a normal budget of eight attempts,
with exponential backoff and equal jitter. Non-2xx responses, network errors and
timeouts retry; redirects are not followed. An interrupted attempt can repeat
after a crash or shutdown, so eight is not an absolute HTTP-request maximum.
Ordering is per connection/destination: a slow target blocks later events for
that target, while other destinations/accounts continue. Exhausted deliveries
remain inspectable rather than disappearing.

Changing a device URL pauses unstarted old-configuration work. Secret-only rotation
applies on the next attempt for the same URL; an in-flight request may still use
the previous secret. After restart, removed global destinations pause and an
unchanged global URL uses its current secret. Original destination snapshots
remain in the delivery ledger.

Use the same selected device for inspection and administrative recovery:

```sh
curl --user "$APP_BASIC_AUTH" -H 'X-Device-Id: support' \
  'http://127.0.0.1:3000/deliveries?limit=20'
curl --user "$APP_BASIC_AUTH" -H 'X-Device-Id: support' -X POST \
  http://127.0.0.1:3000/deliveries/DELIVERY_ID/retry
curl --user "$APP_BASIC_AUTH" -H 'X-Device-Id: support' -X POST \
  http://127.0.0.1:3000/deliveries/DELIVERY_ID/replay
```

Retry replaces a `failed` or `retry` delivery for its unchanged destination with a
new ledger entry at the original durable queue position. A waiting later event
cannot overtake that retry; events already delivered cannot be reordered.
A changed destination requires explicit replay, which appends deliveries for
current targets and can redeliver to a previously successful target. Neither
changes the event ID. Inspect the outcome before replaying again.

## Minimal durable receiver

Save as `receiver.py` and run with a privately configured `WEBHOOK_SECRET`:
`python3 receiver.py`. This example uses only Python's standard library and
listens on localhost. It accepts bounded bodies, verifies signatures and commits
an SQLite inbox before acknowledging. Protect its files; they contain messages.

```python
import hashlib
import hmac
import json
import os
import sqlite3
from http.server import BaseHTTPRequestHandler, HTTPServer

os.umask(0o077)
secret = os.environ["WEBHOOK_SECRET"].encode()
if not secret:
    raise SystemExit("WEBHOOK_SECRET must not be empty")
db = sqlite3.connect("webhook-inbox.db")
db.execute("PRAGMA journal_mode=WAL")
db.execute("PRAGMA synchronous=FULL")
db.execute("CREATE TABLE IF NOT EXISTS inbox (event_id TEXT PRIMARY KEY, body BLOB NOT NULL)")
db.commit()


class Receiver(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/events":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_error(400)
            return
        if not 0 < length <= 4 * 1024 * 1024:
            self.send_error(413)
            return
        self.connection.settimeout(5)
        body = self.rfile.read(length)
        expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
        supplied = self.headers.get("X-Hub-Signature-256", "")
        if len(body) != length or not hmac.compare_digest(supplied.encode(), expected.encode()):
            self.send_error(401)
            return
        try:
            event = json.loads(body)
            event_id = event.get("event_id") if isinstance(event, dict) else None
            if not isinstance(event_id, str) or not 0 < len(event_id) <= 256:
                raise ValueError("invalid event ID")
        except (ValueError, UnicodeError):
            self.send_error(400)
            return
        try:
            with db:
                db.execute("INSERT OR IGNORE INTO inbox VALUES (?, ?)", (event_id, body))
        except sqlite3.Error:
            self.send_error(503)
            return
        self.send_response(204)
        self.end_headers()


HTTPServer(("127.0.0.1", 8080), Receiver).serve_forever()
```

Run a separate worker over committed rows. Make business effects transactional
with processing state, or use your own durable outbox. The example does not
implement application authorization, TLS ingress, retention or a production worker.
The repository also includes a [Go receiver](../src/examples/webhookreceiver/main.go)
using Go's HTTP/crypto packages and the project's SQLite driver.
