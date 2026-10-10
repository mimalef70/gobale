package bale

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
)

// Basic contracts describe the original Bale routes alongside its extension
// catalogue. Native validation remains authoritative for provider semantics.
func basicOperations() []domains.OperationContract {
	type field = domains.FieldSchema
	id := field{Type: "string", MinLength: 1, MaxLength: 32, Pattern: "^-?[0-9]+$"}
	peer := field{Type: "object", Properties: map[string]field{"type": {Type: "string", Enum: []json.RawMessage{json.RawMessage(`"user"`), json.RawMessage(`"group"`), json.RawMessage(`"channel"`)}}, "id": {Type: "string", MinLength: 1, MaxLength: 10, Pattern: "^[0-9]+$"}}, Required: []string{"type", "id"}}
	text := func(max int) field { return field{Type: "string", MaxLength: max} }
	array := field{Type: "array", Items: &peer, MaxItems: 100}
	integer := field{Type: "integer"}
	list := []domains.OperationContract{}
	add := func(name, mode string, props map[string]field, required ...string) {
		list = append(list, domains.OperationContract{Operation: name, Mode: mode, Method: "POST", Path: "/operations/" + name, Description: "Bale " + name, Verification: "see_bale_capability_ledger", Request: field{Type: "object", Properties: props, Required: required}})
	}
	add("group.create", "mutation", map[string]field{"title": text(255), "users": array, "kind": text(32), "username": text(64)}, "title")
	add("group.invite", "mutation", map[string]field{"peer": peer, "users": array}, "peer", "users")
	add("group.title", "mutation", map[string]field{"peer": peer, "title": text(255)}, "peer", "title")
	add("group.description", "mutation", map[string]field{"peer": peer, "description": text(16384)}, "peer", "description")
	add("group.remove", "mutation", map[string]field{"peer": peer, "user": peer}, "peer", "user")
	add("message.edit", "mutation", map[string]field{"peer": peer, "message_id": id, "message": text(65536)}, "peer", "message_id", "message")
	add("message.read", "mutation", map[string]field{"peer": peer, "message_id": id, "date": id}, "peer", "date")
	add("message.forward", "mutation", map[string]field{"peer": peer, "source_peer": peer, "message_id": id, "source_date": id, "hide_sender": {Type: "boolean"}}, "peer", "source_peer", "message_id", "source_date")
	add("message.delete", "mutation", map[string]field{"peer": peer, "message_id": id, "date": id, "just_mine": {Type: "boolean"}}, "peer", "message_id", "just_mine")
	for _, name := range []string{"chat.history", "chat.messages"} {
		add(name, "read", map[string]field{"peer": peer, "date": id, "limit": integer, "load_mode": integer}, "peer")
	}
	for _, name := range []string{"chat.list", "chats"} {
		add(name, "read", map[string]field{"date": id, "limit": integer})
	}
	add("group.list", "read", map[string]field{"kind": text(32), "is_owner": {Type: "boolean"}, "limit": integer, "next": text(1024)})
	add("group.members", "read", map[string]field{"peer": peer, "limit": integer, "next": text(1024)}, "peer")
	for _, name := range []string{"group.info", "group.link"} {
		add(name, "read", map[string]field{"peer": peer}, "peer")
	}
	add("contacts.list", "read", map[string]field{})
	add("contacts.search", "read", map[string]field{"query": text(256)}, "query")
	add("account.info", "read", map[string]field{})
	return list
}
