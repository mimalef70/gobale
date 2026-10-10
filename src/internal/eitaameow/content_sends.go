package eitaameow

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

func contentSendOperations() []domains.OperationContract {
	location := map[string]domains.FieldSchema{"peer": peerField(), "latitude": {Type: "number"}, "longitude": {Type: "number"}}
	contact := map[string]domains.FieldSchema{"peer": peerField(), "phone": {Type: "string", MinLength: 6, MaxLength: 16, Pattern: "^\\+?[1-9][0-9]{5,14}$"}, "first_name": {Type: "string", MinLength: 1, MaxLength: 256}, "last_name": textField(256)}
	return []domains.OperationContract{
		{Operation: "send.location", Mode: "mutation", Method: "POST", Path: "/operations/send.location", Schedulable: true, Description: "Send a static Eitaa location; no live tracking", Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: location, Required: []string{"peer", "latitude", "longitude"}}},
		{Operation: "send.contact", Mode: "mutation", Method: "POST", Path: "/operations/send.contact", Schedulable: true, Description: "Send an Eitaa contact with phone and name; no arbitrary vCard", Verification: "offline; live-unverified", Request: domains.FieldSchema{Type: "object", Properties: contact, Required: []string{"peer", "phone", "first_name"}}},
	}
}

type contentSendRequest struct {
	Peer      domains.Peer `json:"peer"`
	Latitude  float64      `json:"latitude"`
	Longitude float64      `json:"longitude"`
	Phone     string       `json:"phone"`
	FirstName string       `json:"first_name"`
	LastName  string       `json:"last_name"`
}

func normalizeContentSend(name string, raw json.RawMessage) error {
	if name != "send.location" && name != "send.contact" {
		return nil
	}
	var p contentSendRequest
	if json.Unmarshal(raw, &p) != nil {
		return domains.E("INVALID_REQUEST", "invalid Eitaa content send", 400)
	}
	if name == "send.location" && (math.IsNaN(p.Latitude) || math.IsNaN(p.Longitude) || math.IsInf(p.Latitude, 0) || math.IsInf(p.Longitude, 0) || p.Latitude < -90 || p.Latitude > 90 || p.Longitude < -180 || p.Longitude > 180) {
		return domains.E("INVALID_REQUEST", "latitude and longitude are out of range", 400)
	}
	if name == "send.contact" && strings.TrimSpace(p.FirstName) == "" {
		return domains.E("INVALID_REQUEST", "contact first_name must not be blank", 400)
	}
	return nil
}

func (c *Client) contentSendCall(ctx context.Context, name string, raw json.RawMessage, rid int64) (json.RawMessage, bool, error) {
	if name != "send.location" && name != "send.contact" {
		return nil, false, nil
	}
	var p contentSendRequest
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, true, err
	}
	peer, err := c.inputPeer(ctx, p.Peer)
	if err != nil {
		return nil, true, err
	}
	media := object{"_": "inputMediaContact", "phone_number": p.Phone, "first_name": p.FirstName, "last_name": p.LastName, "vcard": ""}
	if name == "send.location" {
		media = object{"_": "inputMediaGeoPoint", "geo_point": object{"_": "inputGeoPoint", "lat": p.Latitude, "long": p.Longitude}}
	}
	// These are one sendMedia RPC, without an upload or provider-side draft.
	// Call admits only a gateway-persisted request ID before reaching this path.
	response, err := c.invoke(ctx, "messages.sendMedia", object{"peer": peer, "media": media, "message": "", "random_id": rid}, true, false)
	if err != nil {
		return nil, true, err
	}
	result, err := sendResult(response, rid)
	if err != nil {
		return nil, true, err
	}
	out, err := json.Marshal(result)
	return out, true, err
}
