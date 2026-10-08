package domains

import "time"

// Message is the consumer projection of a message or edit event. Structured
// provider content remains in Event.Payload; unknown actors stay unknown.
type Message struct {
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
