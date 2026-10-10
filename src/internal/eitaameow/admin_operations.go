package eitaameow

import (
	"context"
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
)

func adminOperations() []domains.OperationContract {
	result := []domains.OperationContract{}
	add := func(name, mode string, extra map[string]domains.FieldSchema, required ...string) {
		p := map[string]domains.FieldSchema{"peer": peerField()}
		for k, v := range extra {
			p[k] = v
		}
		result = append(result, domains.OperationContract{Operation: name, Mode: mode, Method: "POST", Path: "/operations/" + name, Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: p, Required: append([]string{"peer"}, required...)}})
	}
	add("group.description", "mutation", map[string]domains.FieldSchema{"description": textField(1024)}, "description")
	for _, name := range []string{"group.signatures", "group.protect_content"} {
		add(name, "mutation", map[string]domains.FieldSchema{"enabled": {Type: "boolean"}}, "enabled")
	}
	add("group.username.available", "read", map[string]domains.FieldSchema{"username": {Type: "string", MinLength: 1, MaxLength: 32, Pattern: "^[A-Za-z][A-Za-z0-9_]*$"}}, "username")
	add("group.online", "read", map[string]domains.FieldSchema{})
	return result
}

type adminRequest struct {
	Peer        domains.Peer `json:"peer"`
	Description string       `json:"description"`
	Enabled     bool         `json:"enabled"`
	Username    string       `json:"username"`
}

func normalizeAdmin(name string, raw json.RawMessage) error {
	found := false
	for _, op := range adminOperations() {
		if name == op.Operation {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	var p adminRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid group request", 400)
	}
	if p.Peer.Type != "group" && p.Peer.Type != "channel" {
		return domains.E("INVALID_PEER", "operation requires a group or channel", 400)
	}
	return nil
}
func (c *Client) adminCall(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, bool, error) {
	found := false
	for _, op := range adminOperations() {
		if name == op.Operation {
			found = true
			break
		}
	}
	if !found {
		return nil, false, nil
	}
	var p adminRequest
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, true, e
	}
	peer, e := c.inputPeer(ctx, p.Peer)
	if e != nil {
		return nil, true, e
	}
	method := ""
	params := object{}
	write := true
	switch name {
	case "group.description":
		method = "messages.editChatAbout"
		params = object{"peer": peer, "about": p.Description}
	case "group.protect_content":
		method = "messages.toggleNoForwards"
		params = object{"peer": peer, "enabled": p.Enabled}
	case "group.signatures", "group.username.available":
		if peer.str("_") != "inputPeerChannel" {
			return nil, true, domains.Unsupported("operation requires a channel or supergroup")
		}
		method = "channels.toggleSignatures"
		params = object{"channel": inputChannel(peer), "enabled": p.Enabled}
		if name == "group.username.available" {
			method = "channels.checkUsername"
			params = object{"channel": inputChannel(peer), "username": p.Username}
			write = false
		}
	case "group.online":
		method = "messages.getOnlines"
		params = object{"peer": peer}
		write = false
	}
	result, e := c.invoke(ctx, method, params, write, false)
	if e != nil {
		return nil, true, e
	}
	out := object{}
	if write {
		accepted := result["acknowledged"] == true
		if name != "group.description" {
			accepted = isUpdatesResponse(result)
		}
		if !accepted {
			return nil, true, &domains.Error{Code: "SEND_UNKNOWN", Message: "Eitaa group mutation acknowledgement is incomplete", HTTP: 502, Ambiguous: true}
		}
		out["acknowledged"] = true
	} else if name == "group.username.available" {
		available, ok := result["acknowledged"].(bool)
		if !ok {
			return nil, true, protocolError()
		}
		out["available"] = available
	} else {
		n := result.num("onlines")
		if result.str("_") != "chatOnlines" || n < 0 {
			return nil, true, protocolError()
		}
		out["online_count"] = n
	}
	b, e := json.Marshal(out)
	return b, true, e
}
