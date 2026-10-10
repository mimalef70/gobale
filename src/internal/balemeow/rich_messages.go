package balemeow

import (
	"context"
	"encoding/json"
	"math"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

type richInlineButton struct {
	Text         string  `json:"text"`
	URL          *string `json:"url"`
	CallbackData *string `json:"callback_data"`
	CopyText     *string `json:"copy_text"`
	WebAppURL    *string `json:"web_app_url"`
}
type richReplyButton struct {
	Text            string  `json:"text"`
	RequestContact  *bool   `json:"request_contact"`
	RequestLocation *bool   `json:"request_location"`
	WebAppURL       *string `json:"web_app_url"`
}
type richSendRequest struct {
	Peer           domains.Peer         `json:"peer"`
	RequestID      string               `json:"request_id"`
	ReplyMessageID string               `json:"reply_message_id"`
	Silent         bool                 `json:"silent"`
	Name           string               `json:"name"`
	Phones         []string             `json:"phones"`
	Emails         []string             `json:"emails"`
	Latitude       *float64             `json:"latitude"`
	Longitude      *float64             `json:"longitude"`
	Text           string               `json:"text"`
	InlineKeyboard [][]richInlineButton `json:"inline_keyboard"`
	ReplyKeyboard  [][]richReplyButton  `json:"reply_keyboard"`
	RemoveKeyboard *bool                `json:"remove_keyboard"`
	Selective      *bool                `json:"selective"`
}

func richText(value string, maximum int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maximum
}
func richButtonURL(value string, httpsOnly bool) bool {
	if len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	u, e := url.Parse(value)
	return e == nil && u.Hostname() != "" && u.User == nil && u.Opaque == "" && (u.Scheme == "https" || (!httpsOnly && u.Scheme == "http"))
}

// richMessage validates and builds the complete message before resolving a
// provider peer. JSON messages are constructed from a fixed projection; callers
// cannot submit arbitrary raw provider JSON, access hashes or bank payloads.
func richMessage(op string, p richSendRequest, rid int64) (*wire.Message, error) {
	bad := func() (*wire.Message, error) {
		return nil, boundedError("INVALID_REQUEST", "invalid rich message fields", 400)
	}
	switch op {
	case "send.contact":
		if !richText(p.Name, 128) || len(p.Phones) < 1 || len(p.Phones) > 10 || len(p.Emails) > 10 {
			return bad()
		}
		phones := []string{}
		emails := []string{}
		seen := map[string]bool{}
		for _, phone := range p.Phones {
			n, e := normalizePhone(phone)
			if e != nil || seen[n] {
				return bad()
			}
			seen[n] = true
			phones = append(phones, n)
		}
		seen = map[string]bool{}
		for _, email := range p.Emails {
			if len(email) > 254 || !utf8.ValidString(email) {
				return bad()
			}
			v, e := mail.ParseAddress(email)
			if e != nil || v.Address != email || seen[email] {
				return bad()
			}
			seen[email] = true
			emails = append(emails, email)
		}
		data, e := json.Marshal(map[string]any{"dataType": "contact", "data": map[string]any{"contact": map[string]any{"name": p.Name, "phones": phones, "emails": emails, "photo": ""}}})
		if e != nil {
			return bad()
		}
		return &wire.Message{Json: &wire.JSONMessage{RawJson: string(data)}}, nil
	case "send.location":
		if p.Latitude == nil || p.Longitude == nil || math.IsNaN(*p.Latitude) || math.IsNaN(*p.Longitude) || math.IsInf(*p.Latitude, 0) || math.IsInf(*p.Longitude, 0) || math.Abs(*p.Latitude) > 90 || math.Abs(*p.Longitude) > 180 {
			return bad()
		}
		data, e := json.Marshal(map[string]any{"dataType": "location", "data": map[string]any{"location": map[string]any{"latitude": *p.Latitude, "longitude": *p.Longitude}}})
		if e != nil {
			return bad()
		}
		return &wire.Message{Json: &wire.JSONMessage{RawJson: string(data)}}, nil
	case "send.template":
		if !richText(p.Text, 16384) {
			return bad()
		}
		// Template IDs belong to existing provider templates. A newly composed
		// keyboard leaves the field unset, as the reference SDK does; the outer
		// SendMessage RID remains our persisted idempotent message identity.
		t := &wire.TemplateMessage{Message: &wire.Message{Text: &wire.TextMessage{Text: p.Text}}}
		modes := 0
		total := 0
		if p.InlineKeyboard != nil {
			modes++
			if len(p.InlineKeyboard) < 1 || len(p.InlineKeyboard) > 20 {
				return bad()
			}
			t.InlineKeyboard = &wire.InlineKeyboard{}
			for _, row := range p.InlineKeyboard {
				if len(row) < 1 || len(row) > 8 {
					return bad()
				}
				total += len(row)
				r := &wire.InlineRow{}
				for _, b := range row {
					if !richText(b.Text, 64) {
						return bad()
					}
					v := &wire.InlineButton{Text: b.Text}
					actions := 0
					if b.URL != nil {
						actions++
						if !richButtonURL(*b.URL, false) {
							return bad()
						}
						v.Url = &wire.StringValue{Value: *b.URL}
					}
					if b.CallbackData != nil {
						actions++
						if !richText(*b.CallbackData, 64) || len(*b.CallbackData) > 64 {
							return bad()
						}
						v.CallbackData = &wire.StringValue{Value: *b.CallbackData}
					}
					if b.CopyText != nil {
						actions++
						if !richText(*b.CopyText, 256) {
							return bad()
						}
						v.CopyText = &wire.CopyTextButton{Text: *b.CopyText}
					}
					if b.WebAppURL != nil {
						actions++
						if !richButtonURL(*b.WebAppURL, true) {
							return bad()
						}
						v.WebApp = &wire.WebAppButton{Url: *b.WebAppURL}
					}
					if actions != 1 {
						return bad()
					}
					r.Buttons = append(r.Buttons, v)
				}
				t.InlineKeyboard.Rows = append(t.InlineKeyboard.Rows, r)
			}
		}
		if p.ReplyKeyboard != nil {
			modes++
			if len(p.ReplyKeyboard) < 1 || len(p.ReplyKeyboard) > 20 {
				return bad()
			}
			t.ReplyKeyboard = &wire.ReplyKeyboard{}
			for _, row := range p.ReplyKeyboard {
				if len(row) < 1 || len(row) > 8 {
					return bad()
				}
				total += len(row)
				r := &wire.ReplyRow{}
				for _, b := range row {
					if !richText(b.Text, 64) {
						return bad()
					}
					v := &wire.ReplyButton{Text: b.Text}
					actions := 0
					if b.RequestContact != nil {
						actions++
						if !*b.RequestContact {
							return bad()
						}
						v.RequestContact = &wire.BoolValue{Value: true}
					}
					if b.RequestLocation != nil {
						actions++
						if !*b.RequestLocation {
							return bad()
						}
						v.RequestLocation = &wire.BoolValue{Value: true}
					}
					if b.WebAppURL != nil {
						actions++
						if !richButtonURL(*b.WebAppURL, true) {
							return bad()
						}
						v.WebApp = &wire.WebAppButton{Url: *b.WebAppURL}
					}
					if actions > 1 {
						return bad()
					}
					r.Buttons = append(r.Buttons, v)
				}
				t.ReplyKeyboard.Rows = append(t.ReplyKeyboard.Rows, r)
			}
		}
		if p.RemoveKeyboard != nil {
			modes++
			if !*p.RemoveKeyboard {
				return bad()
			}
			t.RemoveKeyboard = &wire.KeyboardRemoval{RemoveKeyboard: true}
			if p.Selective != nil {
				t.RemoveKeyboard.Selective = &wire.BoolValue{Value: *p.Selective}
			}
		} else if p.Selective != nil {
			return bad()
		}
		if modes != 1 || total > 100 {
			return bad()
		}
		return &wire.Message{Template: t}, nil
	default:
		return nil, domains.Unsupported(op)
	}
}

func (c *Client) richMessageCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p richSendRequest
	if len(raw) > 128<<10 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid rich message body", 400)
	}
	rid, err := positiveID(p.RequestID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before sending", 400)
	}
	message, err := richMessage(op, p, rid)
	if err != nil {
		return nil, err
	}
	var replyID int64
	if p.ReplyMessageID != "" {
		replyID, err = messageID(p.ReplyMessageID)
		if err != nil {
			return nil, boundedError("INVALID_REQUEST", "reply_message_id must be a nonzero signed int64 string", 400)
		}
	}
	peer, err := c.messagePeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}
	request := &wire.SendMessageRequest{Peer: peer, ExPeer: extendedPeer(peer, c.canonicalPeer(p.Peer)), Rid: rid, Message: message, IsSilent: p.Silent}
	if replyID != 0 {
		request.QuotedMessage = &wire.MessageReference{Peer: peer, Rid: replyID}
	}
	data, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "SendMessage", request)
	if err != nil {
		return nil, err
	}
	response := &wire.SendMessageResponse{}
	if decode(data, response) != nil || response.Date <= 0 {
		return nil, ambiguous()
	}
	return json.Marshal(map[string]any{"acknowledged": true, "message_id": p.RequestID, "date": time.UnixMilli(response.Date).UTC()})
}
