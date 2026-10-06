package balemeow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

const storyService = "bale.story.v1.Story"

func (c *Client) storyExtended(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		StoryID        string  `json:"story_id"`
		GetUnmutual    *bool   `json:"get_unmutual"`
		Page           int32   `json:"page"`
		Limit          int32   `json:"limit"`
		Text           string  `json:"text"`
		MediaID        string  `json:"media_id"`
		ExpirationDays int32   `json:"expiration_days"`
		Privacy        string  `json:"privacy"`
		TagIDs         []int32 `json:"tag_ids"`
		StoryType      string  `json:"story_type"`
		ReactionType   string  `json:"reaction_type"`
		ReactionText   *string `json:"reaction_text"`
		RequestID      string  `json:"request_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 64<<10 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return nil, groupRequestError("invalid story operation body")
	}
	switch op {
	case "story.add", "story.delete", "story.react":
		if _, err := positiveID(p.RequestID); err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a positive int64 request_id before story mutation", 400)
		}
	case "story.list", "story.get", "story.viewers":
	default:
		return nil, domains.Unsupported(op)
	}
	if op != "story.list" && op != "story.add" && !validStoryID(p.StoryID) {
		return nil, groupRequestError("story_id must be a nonempty opaque string of at most 512 bytes")
	}
	switch op {
	case "story.add":
		if (p.Text == "") == (p.MediaID == "") || (p.Text != "" && !validPollText(p.Text, 4096)) || len(p.MediaID) > 128 {
			return nil, groupRequestError("supply either text of 1..4096 characters or an account-scoped image media_id")
		}
		if p.ExpirationDays == 0 {
			p.ExpirationDays = 1
		}
		if p.ExpirationDays != 1 && p.ExpirationDays != 2 {
			return nil, groupRequestError("expiration_days must be 1 or 2")
		}
		privacy, ok := map[string]int32{"": 0, "unknown": 0, "exclude": 1, "include": 2, "contacts": 3, "all": 4}[p.Privacy]
		if !ok {
			return nil, groupRequestError("privacy must be unknown, exclude, include, contacts or all")
		}
		if len(p.TagIDs) > 32 {
			return nil, groupRequestError("tag_ids permits at most 32 distinct positive int32 values")
		}
		seen := map[int32]bool{}
		for _, id := range p.TagIDs {
			if id <= 0 || seen[id] {
				return nil, groupRequestError("tag_ids permits at most 32 distinct positive int32 values")
			}
			seen[id] = true
		}
		request := &wire.StoryAddRequest{TagIds: p.TagIDs, ExpirationType: p.ExpirationDays - 1, ExceptionType: privacy}
		if p.MediaID != "" {
			location, size, err := c.uploadNativeImage(ctx, p.MediaID, &wire.SendType{Type: 1})
			if err != nil {
				return nil, err
			}
			request.Media = &wire.StoryMedia{FileLocation: location, FileSize: size}
		} else {
			request.Text = &wire.StoryText{Text: p.Text}
		}
		data, err := c.rpc(ctx, storyService, "AddStory", request)
		if err != nil {
			return nil, err
		}
		response := &wire.StoryAddResponse{}
		if decode(data, response) != nil || !validStoryID(response.StoryId) {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "story_id": response.StoryId})
	case "story.delete":
		data, err := c.rpc(ctx, storyService, "RemoveStory", &wire.StoryIDRequest{StoryId: p.StoryID})
		if err != nil {
			return nil, err
		}
		if decode(data, &wire.Empty{}) != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "story_id": p.StoryID})
	case "story.react":
		typ, ok := map[string]int32{"": 0, "user": 0, "channel": 1, "bot": 2}[p.StoryType]
		if !ok {
			return nil, groupRequestError("story_type must be user, channel or bot")
		}
		reaction, ok := map[string]int32{"view": 1, "emoji": 2, "remove_emoji": 3, "link": 4}[p.ReactionType]
		if !ok {
			return nil, groupRequestError("reaction_type must be view, emoji, remove_emoji or link")
		}
		if p.ReactionText != nil && (!utf8.ValidString(*p.ReactionText) || utf8.RuneCountInString(*p.ReactionText) > 128) {
			return nil, groupRequestError("reaction_text permits at most 128 Unicode characters")
		}
		if reaction == 2 && (p.ReactionText == nil || strings.TrimSpace(*p.ReactionText) == "") {
			return nil, groupRequestError("emoji reaction requires reaction_text")
		}
		if (reaction == 1 || reaction == 3) && p.ReactionText != nil {
			return nil, groupRequestError("view and remove_emoji reactions must omit reaction_text")
		}
		request := &wire.StoryReactRequest{StoryId: p.StoryID, Type: typ, ReactionType: reaction}
		if p.ReactionText != nil {
			request.ReactionText = &wire.StringValue{Value: *p.ReactionText}
		}
		data, err := c.rpc(ctx, storyService, "ReactToStory", request)
		if err != nil {
			return nil, err
		}
		if decode(data, &wire.Empty{}) != nil {
			return nil, ambiguous()
		}
		return json.Marshal(map[string]any{"acknowledged": true, "story_id": p.StoryID})
	case "story.list":
		request := &wire.StoryListRequest{}
		if p.GetUnmutual != nil {
			request.GetUnmutual = &wire.BoolValue{Value: *p.GetUnmutual}
		}
		data, err := c.readRPC(ctx, storyService, "GetStories", request)
		if err != nil {
			return nil, err
		}
		response := &wire.StoryListResponse{}
		if decode(data, response) != nil || len(response.Result) > 5000 {
			return nil, protocolError()
		}
		stories := make([]map[string]any, 0, len(response.Result))
		seen := map[string]bool{}
		for _, story := range response.Result {
			if story == nil || seen[story.Id] {
				return nil, protocolError()
			}
			seen[story.Id] = true
			value, e := safeStory(story)
			if e != nil {
				return nil, e
			}
			stories = append(stories, value)
		}
		return json.Marshal(map[string]any{"stories": stories})
	case "story.get":
		data, err := c.readRPC(ctx, storyService, "GetStoryById", &wire.StoryIDRequest{StoryId: p.StoryID})
		if err != nil {
			return nil, err
		}
		response := &wire.StorySingleResponse{}
		if decode(data, response) != nil {
			return nil, protocolError()
		}
		if response.ChannelStory != nil || response.BotStory != nil {
			return nil, domains.Unsupported("channel and bot story result content")
		}
		if response.Story == nil {
			return nil, boundedError("STORY_NOT_FOUND", "story is absent from the provider response", 404)
		}
		if response.Story.Id != p.StoryID {
			return nil, protocolError()
		}
		value, err := safeStory(response.Story)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	case "story.viewers":
		if p.Page == 0 {
			p.Page = 1
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Page < 1 || p.Page > 100000 || p.Limit < 1 || p.Limit > 100 {
			return nil, groupRequestError("page must be 1..100000 and limit 1..100")
		}
		data, err := c.readRPC(ctx, storyService, "GetViewers", &wire.StoryViewersRequest{StoryId: p.StoryID, Pagination: &wire.StoryPagination{Page: p.Page, Limit: p.Limit}})
		if err != nil {
			return nil, err
		}
		response := &wire.StoryViewersResponse{}
		if decode(data, response) != nil || len(response.Viewers) > int(p.Limit) || response.ViewCount < 0 || response.LikeCount < 0 || response.LinkClickCount < 0 || response.EmojiCount < 0 || response.RestoryCount < 0 {
			return nil, protocolError()
		}
		viewers := make([]map[string]any, 0, len(response.Viewers))
		for _, v := range response.Viewers {
			if v == nil || v.UserId == 0 || v.ReactedAt < 0 || len(v.Reaction) > 512 || !utf8.ValidString(v.Reaction) {
				return nil, protocolError()
			}
			reactions, e := safeStoryReactions(v.ReactionData)
			if e != nil {
				return nil, e
			}
			viewers = append(viewers, map[string]any{"user_id": strconv.FormatUint(uint64(v.UserId), 10), "reacted_at": strconv.FormatInt(v.ReactedAt, 10), "reaction": v.Reaction, "reactions": reactions})
		}
		return json.Marshal(map[string]any{"story_id": p.StoryID, "viewers": viewers, "page": p.Page, "limit": p.Limit, "view_count": response.ViewCount, "like_count": response.LikeCount, "link_click_count": response.LinkClickCount, "emoji_count": response.EmojiCount, "restory_count": response.RestoryCount})
	}
	return nil, domains.Unsupported(op)
}

func validStoryID(id string) bool {
	if len(id) == 0 || len(id) > 512 || !utf8.ValidString(id) || strings.TrimSpace(id) == "" {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeStoryReactions(items []*wire.StoryReaction) ([]map[string]any, error) {
	if len(items) > 100 {
		return nil, protocolError()
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item == nil || item.Type < 0 || len(item.GetText().GetValue()) > 512 || !utf8.ValidString(item.GetText().GetValue()) {
			return nil, protocolError()
		}
		result = append(result, map[string]any{"type": item.Type, "text": item.GetText().GetValue()})
	}
	return result, nil
}

func safeStory(story *wire.StoryRecord) (map[string]any, error) {
	if story == nil || !validStoryID(story.Id) || story.OwnerUserId == 0 || story.CreatedAt < 0 || len(story.TagIds) > 100 || len(story.GetReaction().GetValue()) > 512 || !utf8.ValidString(story.GetReaction().GetValue()) {
		return nil, protocolError()
	}
	reactions, err := safeStoryReactions(story.Reactions)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"story_id": story.Id, "owner_user_id": strconv.FormatUint(uint64(story.OwnerUserId), 10), "created_at": strconv.FormatInt(story.CreatedAt, 10), "content_type": story.ContentType, "has_widget": story.HasWidget, "has_reply": story.HasReply, "hidden": story.IsHidden, "mutual": story.IsMutual, "tag_ids": story.TagIds, "privacy": story.ExceptionType, "reaction": story.GetReaction().GetValue(), "reactions": reactions}
	if story.Content == nil {
		result["content_supported"] = false
		return result, nil
	}
	if text := story.Content.Text; text != nil {
		if !utf8.ValidString(text.Text) || len(text.Text) > 64<<10 {
			return nil, protocolError()
		}
		result["text"] = text.Text
	}
	if media := story.Content.Media; media != nil {
		if media.FileSize < 0 {
			return nil, protocolError()
		}
		metadata := map[string]any{"file_size": strconv.FormatInt(media.FileSize, 10), "kind": "image", "download_supported": false}
		if video := media.Video; video != nil {
			if video.FileSize < 0 || video.Duration < 0 || video.Duration > 86400 || len(video.Format) > 32 {
				return nil, protocolError()
			}
			metadata["kind"] = "video"
			metadata["file_size"] = strconv.FormatInt(video.FileSize, 10)
			metadata["duration"] = video.Duration
			metadata["format"] = video.Format
		}
		result["media"] = metadata
	}
	return result, nil
}
