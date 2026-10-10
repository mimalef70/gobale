package balemeow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

func projectEventMessage(e *domains.Event) {
	if e.Type != "message" && e.Type != "message.edited" {
		return
	}
	var payload map[string]any
	if json.Unmarshal(e.Payload, &payload) != nil {
		return
	}
	display := displayContent(payload)
	kind := contentString(display, "kind")
	m := &domains.Message{
		ID: e.MessageID, ChatID: e.Peer.ID, From: e.SenderID,
		SenderNameStatus: "unavailable", Timestamp: e.Time,
		Body: displayBody(display), Kind: kind,
		Supported: kind != "" && kind != "unsupported" && kind != "empty",
		Media:     displayMedia(display),
	}
	if e.Direction == "incoming" || e.Direction == "outgoing" {
		own := e.Direction == "outgoing"
		m.IsFromMe = &own
	}
	if e.Type == "message.edited" {
		m.OriginalMessageID = e.MessageID
		// An edit supplies its updater, not the original message author.
		m.EditorID = e.SenderID
		m.From = ""
		m.IsFromMe = nil
	}
	if quote, ok := payload["quoted_message"].(map[string]any); ok {
		m.RepliedToID = contentString(quote, "message_id")
		if content, ok := quote["content"].(map[string]any); ok {
			m.QuotedBody = displayBody(displayContent(content))
		}
	}
	if origin, ok := payload["forwarded_from"].(map[string]any); ok {
		m.ForwardedFrom = &domains.MessageOrigin{
			MessageID: contentString(origin, "message_id"), SenderID: contentString(origin, "sender_id"),
			Date: contentString(origin, "date"), Author: contentString(origin, "author_sign"),
		}
		if !domains.CanonicalUserID(m.ForwardedFrom.SenderID) {
			m.ForwardedFrom.SenderID = ""
		}
		if peer, ok := origin["peer"].(map[string]any); ok {
			m.ForwardedFrom.Peer = &domains.Peer{Type: contentString(peer, "type"), ID: contentString(peer, "id")}
		}
	}
	e.Message = m
}

// Keyboard wrappers do not force each consumer to walk the provider's union.
func displayContent(b map[string]any) map[string]any {
	for depth := 0; depth < maxContentDepth; depth++ {
		if b["kind"] != "template" && b["kind"] != "template_response" {
			break
		}
		child, ok := b["content"].(map[string]any)
		if !ok {
			break
		}
		b = child
	}
	return b
}

func contentString(b map[string]any, key string) string {
	s, _ := b[key].(string)
	return s
}

func displayBody(b map[string]any) string {
	if s := contentString(b, "message"); s != "" {
		return s
	}
	if s := contentString(b, "caption"); s != "" {
		return s
	}
	switch contentString(b, "kind") {
	case "poll":
		lines := []string{contentString(b, "question")}
		if lines[0] == "" {
			lines[0] = "[Poll]"
		}
		if options, ok := b["options"].([]any); ok {
			for i, option := range options {
				if value, ok := option.(map[string]any); ok {
					lines = append(lines, fmt.Sprintf("%d. %s", i+1, contentString(value, "text")))
				}
			}
		}
		return strings.Join(lines, "\n")
	case "document":
		if name := contentString(b, "name"); name != "" {
			return name
		}
		switch contentString(b, "media_type") {
		case "voice":
			return "[Voice message]"
		case "audio":
			return "[Audio]"
		case "image":
			return "[Image]"
		case "video":
			return "[Video]"
		case "animation":
			return "[Animation]"
		default:
			return "[File]"
		}
	case "contact":
		if name := contentString(b, "name"); name != "" {
			return "[Contact] " + name
		}
		return "[Contact]"
	case "location":
		return fmt.Sprintf("[Location] %v, %v", b["latitude"], b["longitude"])
	case "sticker":
		return "[Sticker]"
	case "gift", "gold_gift":
		return "[Gift]"
	case "service":
		return "[Service message]"
	case "text":
		return "[Empty text message]"
	case "empty":
		return "[Empty message]"
	case "template", "template_response":
		return "[Template]"
	default:
		return "[Unsupported message]"
	}
}

func displayMedia(b map[string]any) *domains.MessageMedia {
	if b["kind"] == "sticker" {
		for _, key := range []string{"animation", "image512", "image256"} {
			if image, ok := b[key].(map[string]any); ok {
				m := mediaMetadata(image)
				m.Type = "sticker"
				return m
			}
		}
		return &domains.MessageMedia{Type: "sticker"}
	}
	if b["kind"] != "document" {
		return nil
	}
	m := mediaMetadata(b)
	m.Type = contentString(b, "media_type")
	if m.Type == "" {
		m.Type = "file"
	}
	return m
}

func mediaMetadata(b map[string]any) *domains.MessageMedia {
	m := &domains.MessageMedia{FileID: contentString(b, "file_id"), Name: contentString(b, "name"), MIMEType: contentString(b, "mime_type")}
	if size, ok := b["size"].(float64); ok {
		m.Size = int64(size)
	}
	m.DownloadSupported, _ = b["download_supported"].(bool)
	return m
}
