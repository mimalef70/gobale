package domains

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WebhookFilter limits new deliveries, never durable event acceptance.
type WebhookFilter struct {
	Peers            []Peer   `json:"peers,omitempty"`
	ExcludePeers     []Peer   `json:"exclude_peers,omitempty"`
	PeerTypes        []string `json:"peer_types,omitempty"`
	SenderIDs        []string `json:"sender_ids,omitempty"`
	ExcludeSenderIDs []string `json:"exclude_sender_ids,omitempty"`
	Directions       []string `json:"directions,omitempty"`
}

func (f *WebhookFilter) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return E("INVALID_WEBHOOK_FILTER", "webhook_filter must be an object", 400)
	}
	type plain WebhookFilter
	var v plain
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return E("INVALID_WEBHOOK_FILTER", "invalid webhook filter", 400)
	}
	*f = WebhookFilter(v)
	return f.Validate()
}
func CanonicalUserID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 32)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
func FilterPeer(key string) (Peer, error) {
	parts := strings.Split(key, ":")
	if len(parts) != 2 {
		return Peer{}, E("INVALID_FILTER", "peer must be type:id", 400)
	}
	p := Peer{Type: parts[0], ID: parts[1]}
	if p.Validate() != nil || !ValidOpaqueID(p.ID) {
		return Peer{}, E("INVALID_FILTER", "peer must contain a valid provider ID", 400)
	}
	return p, nil
}
func validSet(values []string, valid func(string) bool) bool {
	if len(values) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] || !valid(v) {
			return false
		}
		seen[v] = true
	}
	return true
}
func peerType(v string) bool {
	return v == "user" || v == "group" || v == "channel" || v == "bot" || v == "service"
}
func DirectionValid(v string) bool { return v == "incoming" || v == "outgoing" || v == "unknown" }
func (f WebhookFilter) Validate() error {
	for _, list := range [][]Peer{f.Peers, f.ExcludePeers} {
		seen := map[string]bool{}
		if len(list) > 100 {
			return E("INVALID_WEBHOOK_FILTER", "too many peers", 400)
		}
		for _, p := range list {
			if !peerType(p.Type) || !ValidOpaqueID(p.ID) || seen[p.Key()] {
				return E("INVALID_WEBHOOK_FILTER", "invalid or duplicate peer", 400)
			}
			seen[p.Key()] = true
		}
	}
	if !validSet(f.PeerTypes, peerType) || !validSet(f.SenderIDs, ValidOpaqueID) || !validSet(f.ExcludeSenderIDs, ValidOpaqueID) || !validSet(f.Directions, DirectionValid) {
		return E("INVALID_WEBHOOK_FILTER", "invalid or duplicate filter value", 400)
	}
	return nil
}
func contains(v []string, w string) bool {
	for _, x := range v {
		if x == w {
			return true
		}
	}
	return false
}
func hasPeer(v []Peer, w Peer) bool {
	for _, p := range v {
		if p.Key() == w.Key() {
			return true
		}
	}
	return false
}

// EventDirection treats historical missing/zero message actors conservatively.
func EventDirection(e Event) string {
	if (e.Type == "message" || e.Type == "message.edited") && (!ValidOpaqueID(e.SenderID) || ((e.Provider == ProviderBale || e.Provider == "") && !CanonicalUserID(e.SenderID))) {
		return "unknown"
	}
	if !DirectionValid(e.Direction) {
		return "unknown"
	}
	return e.Direction
}
func (f WebhookFilter) Matches(e Event) bool {
	if hasPeer(f.ExcludePeers, e.Peer) || contains(f.ExcludeSenderIDs, e.SenderID) {
		return false
	}
	return (len(f.Peers) == 0 || hasPeer(f.Peers, e.Peer)) && (len(f.PeerTypes) == 0 || contains(f.PeerTypes, e.Peer.Type)) && (len(f.SenderIDs) == 0 || contains(f.SenderIDs, e.SenderID)) && (len(f.Directions) == 0 || contains(f.Directions, EventDirection(e)))
}
func EventAllowed(event string, filters []string) bool {
	return len(filters) == 0 || contains(filters, "*") || contains(filters, event)
}

type OperationFilter struct {
	State, Kind, Operation, Peer, ScheduleID string
	CreatedAfter, CreatedBefore              *time.Time
	Limit, Offset                            int
}
type ScheduleFilter struct {
	State, Kind, Operation, Peer string
	CreatedAfter, CreatedBefore  *time.Time
	Limit, Offset                int
}
type EventFilter struct {
	Peer, Search, Event, Direction, SenderID string
	StartTime, EndTime                       *time.Time
	MediaOnly                                bool
	Limit, Offset                            int
}
type ScheduleOccurrence struct {
	ScheduleID   string    `json:"schedule_id"`
	Number       int       `json:"occurrence_number"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Operation    Operation `json:"operation"`
}

func ValidOperationState(v string) bool {
	return v == "" || contains([]string{"queued", "sending", "succeeded", "failed", "unknown", "cancelled"}, v)
}
func ValidScheduleState(v string) bool {
	return v == "" || contains([]string{"active", "paused", "completed", "failed", "cancelled"}, v)
}
func ValidSendKind(v string) bool {
	return v == "" || contains([]string{"text", "image", "file", "audio", "video", "voice", "operation"}, v)
}
func validTimeRange(a, b *time.Time) bool { return a == nil || b == nil || a.Before(*b) }
func validateWorkFilter(kind, op, peer string, a, b *time.Time) error {
	if !ValidSendKind(kind) || !validTimeRange(a, b) {
		return E("INVALID_FILTER", "invalid kind or time range", 400)
	}
	if op != "" && (!ValidOpaqueID(op) || len(op) > 128) {
		return E("INVALID_FILTER", "invalid operation", 400)
	}
	if peer != "" {
		_, err := FilterPeer(peer)
		return err
	}
	return nil
}
func (f OperationFilter) Validate() error {
	if !ValidOperationState(f.State) {
		return E("INVALID_FILTER", "invalid operation state", 400)
	}
	return validateWorkFilter(f.Kind, f.Operation, f.Peer, f.CreatedAfter, f.CreatedBefore)
}
func (f ScheduleFilter) Validate() error {
	if !ValidScheduleState(f.State) {
		return E("INVALID_FILTER", "invalid schedule state", 400)
	}
	return validateWorkFilter(f.Kind, f.Operation, f.Peer, f.CreatedAfter, f.CreatedBefore)
}
func (f EventFilter) Validate() error {
	if !utf8.ValidString(f.Search) || len(f.Search) > 512 || len(f.Event) > 128 || !validTimeRange(f.StartTime, f.EndTime) || (f.Direction != "" && !DirectionValid(f.Direction)) || (f.SenderID != "" && !ValidOpaqueID(f.SenderID)) {
		return E("INVALID_FILTER", "invalid event filter", 400)
	}
	if f.Peer != "" {
		_, err := FilterPeer(f.Peer)
		return err
	}
	return nil
}
