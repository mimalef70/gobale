package bale

import "github.com/mimalef70/goomni/src/domains"

// OwnMessageProof is deliberately Bale-specific: reviewed Bale sends preserve
// the request RID as the message ID. Other providers cannot inherit this rule.
// Historical durable Bale bodies may predate the explicit provider field.
func OwnMessageProof(account string, event domains.Event, request domains.SendRequest) bool {
	if event.Provider != "" && event.Provider != domains.ProviderBale {
		return false
	}
	if event.Type != "message" || event.Direction != "outgoing" || account == "" || event.AccountID != account || event.SenderID != account || event.MessageID == "" || event.Peer.Validate() != nil || event.Time.UnixMilli() <= 0 {
		return false
	}
	if request.RequestID != event.MessageID || request.Peer.Type != event.Peer.Type || request.Peer.ID != event.Peer.ID {
		return false
	}
	if request.Operation == "" {
		switch request.Kind {
		case "", "text", "image", "file", "audio", "video", "voice":
			return true
		}
	} else if request.Kind == "operation" {
		switch request.Operation {
		case "send.poll", "send.sticker", "send.contact", "send.location", "send.template":
			return true
		}
	}
	return false
}
