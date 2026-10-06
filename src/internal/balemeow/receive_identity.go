package balemeow

import (
	"github.com/mimalef70/gobale/src/domains"
	"strconv"
	"sync/atomic"
	"time"
)

var unsequencedReceipt atomic.Uint64

// Message identity is intrinsic; state notifications can repeat legitimately.
// Dated catch-up uses the same provider timestamp/route as live updates. For an
// undated recovery page the page position is an honest provenance fallback, not
// an invented per-update provider sequence. Consumers must tolerate duplicates.
func stateEventsPosition(events []domains.Event, position string) {
	for i := range events {
		e := &events[i]
		switch e.Type {
		case "message", "message.edited", "message.deleted", "message.accepted", "protocol.unsupported_update":
			continue
		}
		e.ID = eventHash(e.ID + "|position|" + position)
	}
}
func streamPosition(route, sequence int32, stamp int64) string {
	prefix := strconv.FormatInt(int64(route), 10) + "|"
	if stamp > 0 {
		return prefix + "date:" + sid(stamp)
	}
	if sequence > 0 {
		return prefix + "sequence:" + strconv.FormatInt(int64(sequence), 10)
	}
	// Presence packets without a provider position are transient notifications;
	// once persisted, webhook retries reuse the resulting durable event ID.
	return prefix + "receipt:" + strconv.FormatInt(time.Now().UnixNano(), 10) + ":" + strconv.FormatUint(unsequencedReceipt.Add(1), 10)
}

func messageIdentityPeer(p domains.Peer) string {
	if p.Type == "channel" {
		p.Type = "group"
	}
	return p.Key()
}
