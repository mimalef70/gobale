package balemeow

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"sort"
	"strconv"
)

const maxContentDepth = 8

func sid(v int64) string  { return strconv.FormatInt(v, 10) }
func uid(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

// Only explicitly reviewed public fields are projected. Never use protojson on
// provider messages: private media and peer access hashes must stay internal.
func messagePayload(m *wire.Message) json.RawMessage {
	b, _ := json.Marshal(contentBody(m, 0))
	return b
}
func contentBody(m *wire.Message, depth int) map[string]any {
	b := map[string]any{"kind": "unsupported"}
	if m == nil || depth >= maxContentDepth {
		return b
	}
	switch {
	case m.Json != nil:
		b = jsonContentBody(m.Json.RawJson)
	case m.Text != nil:
		b = map[string]any{"kind": "text", "message": m.Text.Text}
		if len(m.Text.Mentions) > 0 {
			ids := []string{}
			for _, v := range m.Text.Mentions {
				ids = append(ids, uid(v))
			}
			b["mentions"] = ids
		}
	case m.Document != nil:
		d := m.Document
		b = map[string]any{"kind": "document", "file_id": sid(d.FileId), "size": d.FileSize, "name": d.Name, "mime_type": d.MimeType, "download_supported": false}
		if d.Caption != nil {
			b["caption"] = d.Caption.Text
			if len(d.Caption.Mentions) > 0 {
				ids := []string{}
				for _, v := range d.Caption.Mentions {
					ids = append(ids, uid(v))
				}
				b["mentions"] = ids
			}
		}
		if x := d.Ext; x != nil {
			switch {
			case x.Photo != nil:
				b["media_type"] = "image"
				b["width"] = x.Photo.Width
				b["height"] = x.Photo.Height
			case x.Video != nil:
				b["media_type"] = "video"
				b["width"] = x.Video.Width
				b["height"] = x.Video.Height
				b["duration"] = x.Video.Duration
			case x.Voice != nil:
				b["media_type"] = "voice"
				b["duration"] = x.Voice.Duration
			case x.Audio != nil:
				b["media_type"] = "audio"
				b["duration"] = x.Audio.Duration
				b["artist"] = x.Audio.Artist
				b["album"] = x.Audio.Album
			case x.Gif != nil:
				b["media_type"] = "animation"
				b["width"] = x.Gif.Width
				b["height"] = x.Gif.Height
				b["duration"] = x.Gif.Duration
			}
		}
	case m.Template != nil:
		t := m.Template
		b = map[string]any{"kind": "template", "template_id": sid(t.Id), "content": contentBody(t.Message, depth+1)}
		if t.ResponseType != nil {
			b["response_type"] = t.ResponseType.Value
		}
		if len(t.Buttons) > 0 {
			buttons := []any{}
			for _, v := range t.Buttons {
				buttons = append(buttons, map[string]any{"text": v.Text, "value": v.Value, "action": v.Action})
			}
			b["buttons"] = buttons
		}
		if t.InlineKeyboard != nil {
			rows := [][]map[string]any{}
			for _, row := range t.InlineKeyboard.Rows {
				buttons := []map[string]any{}
				for _, v := range row.Buttons {
					buttons = append(buttons, inlineButtonBody(v))
				}
				rows = append(rows, buttons)
			}
			b["inline_keyboard"] = rows
		}
		if t.ReplyKeyboard != nil {
			rows := [][]map[string]any{}
			for _, row := range t.ReplyKeyboard.Rows {
				buttons := []map[string]any{}
				for _, v := range row.Buttons {
					x := map[string]any{"text": v.Text}
					if v.RequestContact != nil {
						x["request_contact"] = v.RequestContact.Value
					}
					if v.RequestLocation != nil {
						x["request_location"] = v.RequestLocation.Value
					}
					if v.SendMessage != nil {
						x["send_message"] = v.SendMessage.Value
					}
					if v.WebApp != nil {
						x["web_app"] = map[string]any{"url": v.WebApp.Url}
					}
					if v.CustomAction != nil {
						x["custom_action_id"] = v.CustomAction.Id
					}
					buttons = append(buttons, x)
				}
				rows = append(rows, buttons)
			}
			b["reply_keyboard"] = rows
		}
		if t.RemoveKeyboard != nil {
			b["remove_keyboard"] = t.RemoveKeyboard.RemoveKeyboard
			if t.RemoveKeyboard.Selective != nil {
				b["selective"] = t.RemoveKeyboard.Selective.Value
			}
		}
	case m.TemplateResponse != nil:
		t := m.TemplateResponse
		b = map[string]any{"kind": "template_response", "template_id": sid(t.Id), "response": t.Response, "content": contentBody(t.Message, depth+1)}
	case m.Service != nil:
		b = map[string]any{"kind": "service", "message": m.Service.Text, "actions": serviceActions(m.Service.Ext)}
	case m.Sticker != nil:
		s := m.Sticker
		b = map[string]any{"kind": "sticker", "format": s.Format}
		if s.Id != nil {
			b["sticker_id"] = sid(int64(s.Id.Value))
		}
		if s.CollectionId != nil {
			b["collection_id"] = sid(int64(s.CollectionId.Value))
		}
		for key, img := range map[string]*wire.StickerImage{"image512": s.Image512, "image256": s.Image256, "animation": s.Animation} {
			if img != nil {
				b[key] = stickerImageBody(img)
			}
		}
	case m.AnimatedSticker != nil:
		s := m.AnimatedSticker
		b = map[string]any{"kind": "sticker", "animated": true}
		if s.Id != nil {
			b["sticker_id"] = sid(int64(s.Id.Value))
		}
		if s.CollectionId != nil {
			b["collection_id"] = sid(int64(s.CollectionId.Value))
		}
		if s.FileLocation != nil {
			b["animation"] = stickerImageBody(s.FileLocation)
		}
	case m.Gift != nil:
		g := m.Gift
		b = map[string]any{"kind": "gift", "count": g.Count, "giving_type": g.GivingType, "owner_id": uid(g.OwnerId)}
		// Wallet identifiers are private provider routing information. Respect the
		// sender's hide-total flag even though the encoded amount is present.
		if g.ShowTotalAmount != nil {
			b["show_total_amount"] = g.ShowTotalAmount.Value
			if g.ShowTotalAmount.Value {
				b["total_amount"] = sid(g.TotalAmount)
			}
		}
		if g.Regarding != nil {
			b["message"] = g.Regarding.Value
		}
		if g.CoverId != nil {
			b["cover_id"] = g.CoverId.Value
		}
	case m.GoldGift != nil:
		g := m.GoldGift
		b = map[string]any{"kind": "gold_gift", "gift_id": sid(g.Id), "amount": sid(g.Amount), "count": sid(g.Count), "message": g.Description, "giving_type": g.GivingType}
	case m.Poll != nil:
		p := m.Poll
		b = map[string]any{"kind": "poll", "question": p.Question, "poll_id": sid(p.PollId), "is_anonymous": p.IsAnonymous, "poll_type": p.Type}
		options := []map[string]any{}
		for _, x := range p.Options {
			options = append(options, map[string]any{"id": x.Id, "text": x.Text})
		}
		b["options"] = options
		if r := p.Result; r != nil {
			result := map[string]any{"is_closed": r.IsClosed, "voters_count": r.VotersCount}
			votes := []map[string]any{}
			for _, x := range r.OptionResults {
				votes = append(votes, map[string]any{"option_id": x.OptionId, "votes_count": x.VotesCount})
			}
			result["options"] = votes
			chosen := []string{}
			for _, id := range r.ChosenOptionIds {
				chosen = append(chosen, sid(id))
			}
			result["chosen_option_ids"] = chosen
			// Do not surface voter identities for anonymous polls, even if a malformed
			// provider response includes them.
			if !p.IsAnonymous {
				voters := []string{}
				for _, id := range r.RecentVoters {
					voters = append(voters, sid(id))
				}
				result["recent_voters"] = voters
			}
			b["result"] = result
		}
	case m.Empty != nil:
		b = map[string]any{"kind": "empty"}
	}
	return b
}
func inlineButtonBody(v *wire.InlineButton) map[string]any {
	b := map[string]any{"text": v.Text}
	for k, p := range map[string]*wire.StringValue{"url": v.Url, "callback_data": v.CallbackData, "switch_inline_query": v.SwitchInlineQuery, "switch_inline_query_current_chat": v.SwitchInlineQueryCurrentChat} {
		if p != nil {
			b[k] = p.Value
		}
	}
	if v.CopyText != nil {
		b["copy_text"] = v.CopyText.Text
	}
	if v.WebApp != nil {
		b["web_app"] = map[string]any{"url": v.WebApp.Url}
	}
	if v.LoginUrl != nil {
		x := v.LoginUrl
		b["login_url"] = map[string]any{"url": x.Url, "forward_text": x.ForwardText, "bot_username": x.BotUsername, "request_write_access": x.RequestWriteAccess}
	}
	if v.SwitchInlineQueryChosenChat != nil {
		x := v.SwitchInlineQueryChosenChat
		b["switch_inline_query_chosen_chat"] = map[string]any{"query": x.Query, "allow_user_chats": x.AllowUserChats, "allow_bot_chats": x.AllowBotChats, "allow_group_chats": x.AllowGroupChats, "allow_channel_chats": x.AllowChannelChats}
	}
	if v.Authentication != nil {
		b["authentication"] = true
	}
	if v.CustomAction != nil {
		b["custom_action_id"] = v.CustomAction.Id
	}
	if v.SendData != nil {
		b["send_data"] = v.SendData.Value
	}
	return b
}
func serviceActions(x *wire.ServiceExtensions) []map[string]any {
	out := []map[string]any{}
	if x == nil {
		return out
	}
	add := func(kind string, v map[string]any) { v["type"] = kind; out = append(out, v) }
	for k, p := range map[string]*wire.Empty{"orphaned": x.Orphaned, "avatar_changed": x.AvatarChanged, "chat_archived": x.ChatArchived, "chat_restored": x.ChatRestored, "group_created": x.GroupCreated, "missed_call": x.MissedCall, "user_joined": x.UserJoined, "welcome": x.Welcome, "mini_app_data_sent": x.MiniAppDataSent} {
		if p != nil {
			add(k, map[string]any{})
		}
	}
	for k, p := range map[string]*wire.ServiceTextChange{"about_changed": x.AboutChanged, "nickname_changed": x.NicknameChanged, "topic_changed": x.TopicChanged} {
		if p != nil {
			add(k, map[string]any{"value": p.GetValue().GetValue()})
		}
	}
	if x.TitleChanged != nil {
		add("title_changed", map[string]any{"value": x.TitleChanged.Title})
	}
	for k, p := range map[string]*wire.UserIDUpdate{"contact_registered": x.ContactRegistered, "user_invited": x.UserInvited, "user_removed": x.UserRemoved, "user_left": x.UserLeft} {
		if p != nil {
			add(k, map[string]any{"user_id": uid(p.Uid)})
		}
	}
	if p := x.PhoneCall; p != nil {
		b := map[string]any{"duration": p.Duration, "discard_reason": p.DiscardReason}
		if p.StartDate != nil {
			b["start_date"] = sid(p.StartDate.Value)
		}
		if p.IsVideo != nil {
			b["is_video"] = p.IsVideo.Value
		}
		add("phone_call", b)
	}
	if p := x.GiftOpened; p != nil {
		add("gift_opened", map[string]any{"user_id": uid(p.UserId), "message_id": sid(p.Rid), "date": sid(p.Date)})
	}
	if p := x.GiftOpenedCompact; p != nil {
		add("gift_opened", map[string]any{"last_user_id": uid(p.LastUserId), "others_count": p.OthersCount, "message_id": sid(p.Rid), "date": sid(p.Date)})
	}
	// Stable output is important for edited-message deduplication fingerprints.
	sortActions(out)
	return out
}
func quoteBody(q *wire.QuotedMessage) map[string]any {
	if q == nil {
		return nil
	}
	b := map[string]any{"sender_id": uid(q.SenderUserId), "date": sid(q.Date), "content": contentBody(q.Message, 1)}
	if q.MessageId != nil {
		b["message_id"] = sid(q.MessageId.Value)
	}
	if q.PublicGroupId != nil {
		b["public_group_id"] = sid(int64(q.PublicGroupId.Value))
	}
	if q.AuthorSign != nil {
		b["author_sign"] = q.AuthorSign.Value
	}
	if p, e := decodePeer(q.Peer); e == nil {
		b["peer"] = p
	}
	return b
}
func positionBody(p *wire.MessagePosition) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{"message_id": sid(p.Rid), "date": sid(p.Date)}
}
func decoratedPayload(m *wire.Message, q *wire.QuotedMessage, previous, thread *wire.MessagePosition, grouped *wire.Int64Value, author *wire.StringValue) json.RawMessage {
	b := contentBody(m, 0)
	if q != nil {
		if m.GetEmpty() != nil {
			// An empty current message plus a quote is a provider forward. Its
			// content belongs to the current message; ordinary reply quotes do not.
			b = contentBody(q.Message, 0)
			origin := quoteBody(q)
			delete(origin, "content")
			b["forwarded_from"] = origin
		} else {
			b["quoted_message"] = quoteBody(q)
		}
	}
	if previous != nil {
		b["previous_message"] = positionBody(previous)
	}
	if thread != nil {
		b["thread"] = positionBody(thread)
	}
	if grouped != nil {
		b["grouped_id"] = sid(grouped.Value)
	}
	if author != nil {
		b["author_sign"] = author.Value
	}
	data, _ := json.Marshal(b)
	return data
}
func nestedDocument(m *wire.Message, depth int) *wire.DocumentMessage {
	if m == nil || depth >= maxContentDepth {
		return nil
	}
	if m.Document != nil {
		return m.Document
	}
	if m.Template != nil {
		return nestedDocument(m.Template.Message, depth+1)
	}
	if m.TemplateResponse != nil {
		return nestedDocument(m.TemplateResponse.Message, depth+1)
	}
	return nil
}
func providerMedia(m *wire.Message) *domains.ProviderMedia {
	d := nestedDocument(m, 0)
	if d == nil {
		return nil
	}
	return &domains.ProviderMedia{FileID: sid(d.FileId), AccessHash: sid(d.AccessHash), Size: int64(d.FileSize), Name: d.Name, ContentType: d.MimeType}
}

func messageMedia(m *wire.Message, q *wire.QuotedMessage) *domains.ProviderMedia {
	if m.GetEmpty() != nil && q != nil {
		return providerMedia(q.Message)
	}
	return providerMedia(m)
}

func sortActions(v []map[string]any) {
	sort.Slice(v, func(i, j int) bool { return v[i]["type"].(string) < v[j]["type"].(string) })
}

// prepareEvent is applied once to the connection sink, covering both live and
// recovered updates. The sink commits the private reference with the event.
func (c *Client) prepareEvent(ctx context.Context, e domains.Event) domains.Event {
	e.Peer = c.canonicalPeer(e.Peer)
	var b map[string]any
	if json.Unmarshal(e.Payload, &b) == nil {
		c.canonicalContentPeers(b, 0)
		e.Payload, _ = json.Marshal(b)
	}
	projectEventMessage(&e)
	if e.Message != nil {
		e.Message.SenderDisplayName, e.Message.SenderNameStatus = c.senderDisplayName(ctx, e.Message.From)
	}
	return e
}
func (c *Client) canonicalContentPeers(b map[string]any, depth int) {
	if depth > maxContentDepth+2 {
		return
	}
	if v, ok := b["peer"].(map[string]any); ok {
		kind, _ := v["type"].(string)
		id, _ := v["id"].(string)
		p := c.canonicalPeer(domains.Peer{Type: kind, ID: id})
		v["type"] = p.Type
	}
	for _, key := range []string{"content", "quoted_message", "forwarded_from"} {
		if v, ok := b[key].(map[string]any); ok {
			c.canonicalContentPeers(v, depth+1)
		}
	}
}

func stickerImageBody(img *wire.StickerImage) map[string]any {
	b := map[string]any{"width": img.Width, "height": img.Height, "size": img.FileSize, "download_supported": false}
	if img.File != nil {
		b["file_id"] = sid(img.File.FileId)
	}
	return b
}
