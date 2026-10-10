package bale

import (
	"bytes"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type mutationPayload struct {
	Description *string        `json:"description,omitempty"`
	User        *domains.Peer  `json:"user,omitempty"`
	SourcePeer  *domains.Peer  `json:"source_peer,omitempty"`
	SourceDate  string         `json:"source_date,omitempty"`
	HideSender  *bool          `json:"hide_sender,omitempty"`
	JustMine    *bool          `json:"just_mine,omitempty"`
	Peer        domains.Peer   `json:"peer,omitempty"`
	Users       []domains.Peer `json:"users,omitempty"`
	Title       string         `json:"title,omitempty"`
	Message     string         `json:"message,omitempty"`
	MessageID   string         `json:"message_id,omitempty"`
	Date        string         `json:"date,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Username    *string        `json:"username,omitempty"`
}

func NormalizeMutation(operation string, payload json.RawMessage) (json.RawMessage, domains.Peer, error) {
	if domains.IsExtendedMutation(operation) {
		return domains.NormalizeOperation(operation, payload)
	}
	invalid := func(message string) (json.RawMessage, domains.Peer, error) {
		return nil, domains.Peer{}, domains.E("INVALID_REQUEST", message, 400)
	}
	if len(payload) == 0 || len(payload) > 128<<10 {
		return invalid("mutation payload must be a JSON object up to 128 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var p mutationPayload
	if err := decoder.Decode(&p); err != nil {
		return invalid("mutation payload contains invalid or unsupported fields")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return invalid("mutation payload must contain one JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return invalid("mutation payload must be an object")
	}
	if operation != "message.forward" && (p.SourcePeer != nil || p.SourceDate != "" || p.HideSender != nil) {
		return invalid("forwarding fields require message.forward")
	}
	if operation != "message.delete" && p.JustMine != nil {
		return invalid("just_mine requires message.delete")
	}
	if operation != "group.description" && p.Description != nil {
		return invalid("description requires group.description")
	}
	if operation != "group.remove" && p.User != nil {
		return invalid("user requires group.remove")
	}
	if operation != "group.create" && (p.Kind != "" || p.Username != nil) {
		return invalid("kind and username require group.create")
	}
	if operation == "group.create" {
		if p.Kind != "" && p.Kind != "group" && p.Kind != "channel" && p.Kind != "supergroup" {
			return invalid("unsupported group kind")
		}
		if p.Username != nil && (!utf8.ValidString(*p.Username) || len(*p.Username) > 64) {
			return invalid("invalid username")
		}
	}
	switch operation {
	case "message.forward", "message.delete":
		if p.Peer.Type != "user" && p.Peer.Type != "group" && p.Peer.Type != "channel" {
			return invalid("message mutations require a user, group or channel peer")
		}
		if err := validMutationPeer(p.Peer, p.Peer.Type, false); err != nil {
			return nil, domains.Peer{}, err
		}
		if !ValidMessageID(p.MessageID) {
			return invalid("message_id must be a nonzero signed decimal int64")
		}
		if p.Title != "" || len(p.Users) > 0 || p.Message != "" {
			return invalid("unsupported fields for message mutation")
		}
		if operation == "message.forward" {
			if p.SourcePeer == nil {
				return invalid("source_peer is required for forwarding")
			}
			if p.SourcePeer.Type != "user" && p.SourcePeer.Type != "group" && p.SourcePeer.Type != "channel" {
				return invalid("source_peer must be a user, group or channel")
			}
			if err := validMutationPeer(*p.SourcePeer, p.SourcePeer.Type, false); err != nil {
				return nil, domains.Peer{}, err
			}
			if !positiveInt64(p.SourceDate) {
				return invalid("source_date is required as the original provider timestamp in decimal milliseconds")
			}
			if p.Date != "" {
				return invalid("use source_date when forwarding")
			}
		} else {
			if p.JustMine == nil {
				return invalid("just_mine must explicitly select own-view or everyone deletion")
			}
			if p.Date != "" && !positiveInt64(p.Date) {
				return invalid("date must be a positive decimal millisecond timestamp")
			}
		}
	case "group.create", "group.title", "group.invite", "group.description", "group.remove":
		if p.Message != "" || p.MessageID != "" || p.Date != "" {
			return invalid("message fields are not accepted by group operations")
		}
		if operation == "group.create" {
			if p.Peer.ID != "" || p.Peer.Type != "" || p.Peer.AccessHash != "" {
				return invalid("group.create does not accept an existing peer")
			}
		} else if p.Peer.Type != "group" && p.Peer.Type != "channel" {
			return invalid("a group or channel peer is required")
		} else if err := validMutationPeer(p.Peer, p.Peer.Type, false); err != nil {
			return nil, domains.Peer{}, err
		}
		if operation == "group.create" || operation == "group.title" {
			p.Title = strings.TrimSpace(p.Title)
			if p.Title == "" || !utf8.ValidString(p.Title) || utf8.RuneCountInString(p.Title) > 255 {
				return invalid("title must contain 1 to 255 characters")
			}
		} else if p.Title != "" {
			return invalid("this group operation does not accept a title")
		}
		if (operation == "group.title" || operation == "group.description" || operation == "group.remove") && len(p.Users) > 0 {
			return invalid("this group operation does not accept a users list")
		}
		if operation == "group.description" && (p.Description == nil || !utf8.ValidString(*p.Description) || utf8.RuneCountInString(*p.Description) > 4096) {
			return invalid("description is required and must contain at most 4096 characters; empty clears it")
		}
		if operation == "group.remove" {
			if p.User == nil {
				return invalid("one user peer is required for group removal")
			}
			if err := validMutationPeer(*p.User, "user", false); err != nil {
				return nil, domains.Peer{}, err
			}
		}
		if len(p.Users) > 100 || (operation == "group.invite" && len(p.Users) == 0) {
			return invalid("provide at most 100 users, with at least one for an invitation")
		}
		seen := map[string]bool{}
		for _, peer := range p.Users {
			if err := validMutationPeer(peer, "user", false); err != nil {
				return nil, domains.Peer{}, err
			}
			if seen[peer.ID] {
				return invalid("users must be unique")
			}
			seen[peer.ID] = true
		}
	case "message.edit", "message.read":
		if p.Title != "" || len(p.Users) > 0 {
			return invalid("group fields are not accepted by message operations")
		}
		if p.Peer.Type != "user" && p.Peer.Type != "group" && p.Peer.Type != "channel" {
			return invalid("message operations require a user, group or channel peer")
		}
		if err := validMutationPeer(p.Peer, p.Peer.Type, false); err != nil {
			return nil, domains.Peer{}, err
		}
		if operation == "message.edit" {
			if !ValidMessageID(p.MessageID) || p.Message == "" || len(p.Message) > 65536 || !utf8.ValidString(p.Message) {
				return invalid("message.edit requires a nonzero signed message_id and valid text up to 64 KiB")
			}
			if p.Date != "" {
				return invalid("message.edit does not accept date")
			}
		} else {
			if !positiveInt64(p.Date) {
				return invalid("message.read requires a positive decimal millisecond date")
			}
			if p.Message != "" || (p.MessageID != "" && !ValidMessageID(p.MessageID)) {
				return invalid("message.read accepts an optional nonzero signed contextual message_id and no message text")
			}
		}
	default:
		return nil, domains.Peer{}, domains.Unsupported(operation)
	}
	normalized, err := json.Marshal(p)
	return normalized, p.Peer, err
}
func positiveInt64(value string) bool {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0
}

func ValidMessageID(value string) bool {
	digits := strings.TrimPrefix(value, "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n != 0
}
func validMutationPeer(peer domains.Peer, kind string, requireHash bool) error {
	if peer.Type != kind {
		return domains.E("INVALID_PEER", "invalid peer type for this operation", 400)
	}
	if err := peer.Validate(); err != nil {
		return err
	}
	n, err := strconv.ParseUint(peer.ID, 10, 32)
	if err != nil || n == 0 {
		return domains.E("INVALID_PEER", "peer.id must be a positive uint32 decimal string", 400)
	}
	if peer.AccessHash != "" {
		if _, err = strconv.ParseInt(peer.AccessHash, 10, 64); err != nil {
			return domains.E("INVALID_PEER", "access_hash must be a signed int64 decimal string", 400)
		}
	} else if requireHash {
		return domains.E("PEER_ACCESS_REQUIRED", "user provider access reference has not been resolved", 409)
	}
	return nil
}
