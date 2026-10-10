package eitaameow

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func profileOperations() []domains.OperationContract {
	add := func(name string, props map[string]domains.FieldSchema, required ...string) domains.OperationContract {
		return domains.OperationContract{Operation: name, Mode: "read", Path: "/operations/" + name, Method: "POST", Description: "Eitaa " + name, Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: props, Required: required}}
	}
	return []domains.OperationContract{
		add("users.get", map[string]domains.FieldSchema{"user": idField()}, "user"),
		add("account.username.check", map[string]domains.FieldSchema{"username": {Type: "string", MinLength: 1, MaxLength: 64, Pattern: "^[A-Za-z][A-Za-z0-9_]{0,63}$"}}, "username"),
		add("contacts.status", map[string]domains.FieldSchema{}), add("contacts.ids", map[string]domains.FieldSchema{}), add("contacts.saved", map[string]domains.FieldSchema{}),
	}
}
func (c *Client) profileCall(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, bool, error) {
	known := false
	for _, op := range profileOperations() {
		if op.Operation == name {
			known = true
		}
	}
	if !known {
		return nil, false, nil
	}
	var p struct {
		User, Username, Category string
		Offset, Limit            int
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, true, protocolError()
	}
	method := ""
	params := object{}
	switch name {
	case "users.get":
		user, e := c.inputUser(ctx, p.User)
		if e != nil {
			return nil, true, e
		}
		method = "users.getFullUser"
		params["id"] = user
	case "account.username.check":
		method = "account.checkUsername"
		params["username"] = p.Username
	case "contacts.status":
		method = "contacts.getStatuses"
	case "contacts.ids":
		method = "contacts.getContactIDs"
		params["hash"] = 0
	case "contacts.saved":
		method = "contacts.getSaved"
	}
	response, e := c.invoke(ctx, method, params, false, false)
	if e != nil {
		return nil, true, e
	}
	if e = c.rememberEntities(ctx, response); e != nil {
		return nil, true, e
	}
	var result any
	switch name {
	case "users.get":
		if response.str("_") != "userFull" {
			return nil, true, protocolError()
		}
		user := asObject(response["user"])
		if strconv.FormatInt(user.num("id"), 10) != p.User {
			return nil, true, domains.E("ACCOUNT_CHANGED", "Eitaa returned a different user", 502)
		}
		out := publicUser(user)
		out["about"] = response.str("about")
		for _, key := range []string{"blocked", "can_pin_message"} {
			value, _ := response[key].(bool)
			out[key] = value
		}
		out["common_chats_count"] = response.num("common_chats_count")
		if response.num("pinned_msg_id") > 0 {
			out["pinned_message_id"] = strconv.FormatInt(response.num("pinned_msg_id"), 10)
		}
		result = out
	case "account.username.check":
		available, ok := response["acknowledged"].(bool)
		if !ok {
			return nil, true, protocolError()
		}
		result = object{"available": available}
	case "contacts.status":
		items := []object{}
		for _, row := range asObjects(response["items"]) {
			if row.str("_") != "contactStatus" || row.num("user_id") < 1 {
				return nil, true, protocolError()
			}
			status := asObject(row["status"])
			known := map[string]string{"userStatusEmpty": "unavailable", "userStatusOnline": "online", "userStatusOffline": "offline", "userStatusRecently": "recently", "userStatusLastWeek": "last_week", "userStatusLastMonth": "last_month"}
			state, ok := known[status.str("_")]
			if !ok {
				return nil, true, protocolError()
			}
			out := object{"user_id": strconv.FormatInt(row.num("user_id"), 10), "status": state}
			for _, key := range []string{"expires", "was_online"} {
				if value := status.num(key); value > 0 {
					out[key] = time.Unix(value, 0).UTC().Format(time.RFC3339)
				}
			}
			items = append(items, out)
		}
		result = object{"items": items}
	case "contacts.ids":
		values, ok := response["items"].([]any)
		if !ok {
			return nil, true, protocolError()
		}
		ids := []string{}
		for _, v := range values {
			n := object{"id": v}.num("id")
			if n < 1 {
				return nil, true, protocolError()
			}
			ids = append(ids, strconv.FormatInt(n, 10))
		}
		result = object{"items": ids}
	case "contacts.saved":
		items := []object{}
		for _, row := range asObjects(response["items"]) {
			if row.str("_") != "savedPhoneContact" || !boundedText(row.str("phone"), 128) || !boundedText(row.str("first_name"), 1024) || !boundedText(row.str("last_name"), 1024) {
				return nil, true, protocolError()
			}
			out := object{"phone": row.str("phone"), "first_name": row.str("first_name"), "last_name": row.str("last_name")}
			if row.num("date") > 0 {
				out["date"] = time.Unix(row.num("date"), 0).UTC().Format(time.RFC3339)
			}
			items = append(items, out)
		}
		result = object{"items": items}
	}
	out, e := json.Marshal(result)
	return out, true, e
}
