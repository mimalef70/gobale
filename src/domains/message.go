package domains

import "time"

// Message is the consumer projection of a message or edit event. Structured
// provider content remains in Event.Payload; unknown actors stay unknown.
type Message struct {
	Partial           bool           `json:"partial"`
	ID                string         `json:"id"`
	ChatID            string         `json:"chat_id"`
	From              string         `json:"from"`
	SenderDisplayName string         `json:"sender_display_name"`
	SenderNameStatus  string         `json:"sender_name_status"`
	IsFromMe          *bool          `json:"is_from_me"`
	Timestamp         time.Time      `json:"timestamp"`
	Body              string         `json:"body"`
	Kind              string         `json:"kind"`
	Supported         bool           `json:"supported"`
	RepliedToID       string         `json:"replied_to_id,omitempty"`
	QuotedBody        string         `json:"quoted_body,omitempty"`
	OriginalMessageID string         `json:"original_message_id,omitempty"`
	EditorID          string         `json:"editor_id,omitempty"`
	ForwardedFrom     *MessageOrigin `json:"forwarded_from,omitempty"`
	Media             *MessageMedia  `json:"media,omitempty"`
}

// MessagePatch contains only reviewed changes, never a fabricated full message.
// A nil Body leaves text unchanged; a non-nil empty Body explicitly clears it.
// Unknown attachments must be refreshed, not replaced from an absent field.
type MessagePatch struct {
	ID                string  `json:"id"`
	ChatID            string  `json:"chat_id"`
	OriginalMessageID string  `json:"original_message_id"`
	Partial           bool    `json:"partial"`
	Body              *string `json:"body,omitempty"`
	Supported         bool    `json:"supported"`
}

type MessageOrigin struct {
	MessageID string `json:"message_id,omitempty"`
	SenderID  string `json:"sender_id,omitempty"`
	Peer      *Peer  `json:"peer,omitempty"`
	Date      string `json:"date,omitempty"`
	Author    string `json:"author_sign,omitempty"`
}

// DownloadSupported states that an authenticated private reference was accepted
// by this connection's durable storage, not that a later download will succeed.
type MessageMedia struct {
	Type              string `json:"type"`
	FileID            string `json:"file_id"`
	Name              string `json:"name"`
	MIMEType          string `json:"mime_type"`
	Size              int64  `json:"size"`
	DownloadSupported bool   `json:"download_supported"`
}

// ValidateMessageProjection applies to newly accepted adapter projections only.
// Historical persisted bodies keep their original identity and signed bytes.
func (e Event) ValidateMessageProjection() error {
	invalid := func() error {
		return E("INVALID_MESSAGE_PROJECTION", "provider message projection has inconsistent identity or shape", 502)
	}
	if e.Message != nil && e.MessagePatch != nil {
		return invalid()
	}
	if e.Message != nil {
		m := e.Message
		if (e.Type != "message" && e.Type != "message.edited") || m.Partial || !ValidOpaqueID(e.MessageID) || m.ID != e.MessageID || !ValidOpaqueID(e.Peer.ID) || m.ChatID != e.Peer.ID {
			return invalid()
		}
		if e.Type == "message.edited" && m.OriginalMessageID != e.MessageID {
			return invalid()
		}
	}
	if e.MessagePatch != nil {
		p := e.MessagePatch
		if e.Type != "message.edited" || e.Media != nil || !p.Partial || !ValidOpaqueID(e.MessageID) || p.ID != e.MessageID || p.OriginalMessageID != e.MessageID || !ValidOpaqueID(e.Peer.ID) || p.ChatID != e.Peer.ID || p.Supported != (p.Body != nil) {
			return invalid()
		}
	}
	return nil
}
