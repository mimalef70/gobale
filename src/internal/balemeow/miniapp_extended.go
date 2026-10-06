package balemeow

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Mini app URLs/hashes come from Bale. We do not invent user identity, query IDs
// or unsigned authentication data when the provider rejects a launch request.
func (c *Client) miniAppExtended(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		BotID      string            `json:"bot_id"`
		Peer       domains.Peer      `json:"peer"`
		ScreenMode int32             `json:"screen_mode"`
		Source     string            `json:"source"`
		StartParam *string           `json:"start_param"`
		ShortName  string            `json:"short_name"`
		URL        string            `json:"url"`
		Theme      map[string]string `json:"theme"`
		Data       string            `json:"data"`
		QueryID    string            `json:"query_id"`
		ButtonText string            `json:"button_text"`
		Method     string            `json:"method"`
		Params     string            `json:"params"`
		MessageID  string            `json:"message_id"`
		Date       string            `json:"date_ms"`
		RequestID  string            `json:"request_id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid mini app body", 400)
	}
	contract, ok := domains.OperationDefinition(op)
	if !ok {
		return nil, domains.Unsupported(op)
	}
	if contract.Mode == "mutation" {
		if _, err := positiveID(p.RequestID); err != nil {
			return nil, boundedError("INVALID_REQUEST_ID", "persist a request ID before mutation", 400)
		}
	}
	var bot uint64
	var err error
	if strings.HasPrefix(op, "miniapp.") {
		bot, err = strconv.ParseUint(p.BotID, 10, 32)
		if err != nil || bot == 0 {
			return nil, boundedError("INVALID_REQUEST", "bot_id must be a positive uint32 string", 400)
		}
	}
	var ex *wire.Peer
	if p.Peer.ID != "" {
		ex, err = c.exMessagePeer(ctx, p.Peer)
		if err != nil {
			return nil, err
		}
	}
	service := "bale.appzar.v1.Appzar"
	var method string
	var request proto.Message
	var response proto.Message
	wrap := func(s *string) *wire.StringValue {
		if s == nil {
			return nil
		}
		return &wire.StringValue{Value: *s}
	}
	switch op {
	case "miniapp.url":
		req := &wire.MiniAppURLRequest{BotUserId: uint32(bot), ScreenMode: p.ScreenMode}
		if p.ScreenMode < 0 || p.ScreenMode > 2 {
			return nil, boundedError("INVALID_REQUEST", "screen_mode must be 0 to 2", 400)
		}
		switch p.Source {
		case "", "main":
			req.Main = &wire.MiniAppMain{StartParam: wrap(p.StartParam), Peer: ex}
		case "direct":
			if p.ShortName == "" {
				return nil, boundedError("INVALID_REQUEST", "direct launch requires short_name", 400)
			}
			req.DirectLink = &wire.MiniAppDirect{ShortName: p.ShortName, StartParam: wrap(p.StartParam), Peer: ex}
		case "menu":
			if p.URL != "" && !auxHTTPURL(p.URL) {
				return nil, boundedError("INVALID_REQUEST", "menu URL must be HTTP(S)", 400)
			}
			req.MenuButton = &wire.MiniAppMenu{}
			if p.URL != "" {
				req.MenuButton.Url = &wire.StringValue{Value: p.URL}
			}
		case "keyboard":
			if !auxHTTPURL(p.URL) {
				return nil, boundedError("INVALID_REQUEST", "keyboard launch requires an HTTP(S) URL", 400)
			}
			req.KeyboardButton = &wire.MiniAppKeyboard{Url: p.URL}
		default:
			return nil, boundedError("INVALID_REQUEST", "unknown mini app launch source", 400)
		}
		if len(p.Theme) > 0 {
			req.Theme = &wire.MiniAppTheme{}
			m := req.Theme.ProtoReflect()
			for key, value := range p.Theme {
				field := m.Descriptor().Fields().ByName(protoreflect.Name(key))
				if field == nil {
					return nil, boundedError("INVALID_REQUEST", "unknown theme color", 400)
				}
				m.Set(field, protoreflect.ValueOfMessage((&wire.StringValue{Value: value}).ProtoReflect()))
			}
		}
		method, request, response = "GetMiniAppUrl", req, &wire.MiniAppURLResponse{}
	case "miniapp.hash":
		service, method, request, response = "bale.ketf.v1.Ketf", "GetWebappHash", &wire.MiniAppHashRequest{BotUserId: uint32(bot), Data: p.Data}, &wire.MiniAppHashResponse{}
	case "miniapp.menu":
		method, request, response = "GetMenuButton", &wire.MiniAppBotRequest{BotUserId: uint32(bot)}, &wire.MiniAppMenuResponse{}
	case "miniapp.data":
		service, method, request, response = "bale.ketf.v1.Ketf", "SendMiniAppData", &wire.MiniAppDataRequest{BotUserId: uint32(bot), QueryId: &wire.StringValue{Value: p.QueryID}, Data: &wire.StringValue{Value: p.Data}, ButtonText: &wire.StringValue{Value: p.ButtonText}}, &wire.Empty{}
	case "miniapp.custom":
		method, request, response = "InvokeCustomMethod", &wire.MiniAppCustomRequest{BotUserId: uint32(bot), Method: p.Method, Params: p.Params}, &wire.MiniAppCustomResponse{}
	case "bot.callback":
		rid, e := messageID(p.MessageID)
		date, de := positiveID(p.Date)
		if e != nil || de != nil {
			return nil, boundedError("INVALID_REQUEST", "original message_id and date_ms are required", 400)
		}
		service, method, request, response = "bale.ketf.v1.Ketf", "SendInlineCallback", &wire.MiniAppCallbackRequest{Peer: ex, MessageId: &wire.MessagePosition{Rid: rid, Date: date}, Data: &wire.StringValue{Value: p.Data}}, &wire.Empty{}
	case "link.summary":
		if !auxHTTPURL(p.URL) {
			return nil, boundedError("INVALID_REQUEST", "url must be absolute HTTP(S) without credentials", 400)
		}
		service, method, request, response = "bale.tldr.v1.TLDR", "GetLinkSummary", &wire.LinkSummaryRequest{Url: p.URL}, &wire.LinkSummaryResponse{}
	default:
		return nil, domains.Unsupported(op)
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	rpc := c.rpc
	if contract.Mode == "read" {
		rpc = c.readRPC
	}
	data, err := rpc(ctx, service, method, request)
	if err != nil {
		return nil, err
	}
	if decode(data, response) != nil {
		if contract.Mode == "mutation" {
			return nil, ambiguous()
		}
		return nil, boundedError("PROTOCOL_INVALID", "provider returned an invalid auxiliary response", 502)
	}
	switch r := response.(type) {
	case *wire.MiniAppURLResponse:
		if !auxHTTPURL(r.Url) {
			return nil, boundedError("PROTOCOL_INVALID", "provider did not return a valid launch URL", 502)
		}
		out := map[string]any{"url": r.Url, "screen_mode": r.ScreenMode}
		if r.QueryId != nil {
			out["query_id"] = r.QueryId.Value
		}
		return json.Marshal(out)
	case *wire.MiniAppHashResponse:
		if r.Hash == "" || r.AuthDate <= 0 {
			return nil, boundedError("PROTOCOL_INVALID", "provider returned incomplete mini app authentication data", 502)
		}
		return json.Marshal(map[string]any{"hash": r.Hash, "query_id": r.QueryId, "auth_date": strconv.FormatInt(r.AuthDate, 10)})
	case *wire.MiniAppMenuResponse:
		out := map[string]any{"kind": "none"}
		if r.MenuButton != nil {
			if r.MenuButton.Commands != nil {
				out["kind"] = "commands"
			}
			if r.MenuButton.MiniApp != nil {
				out = map[string]any{"kind": "miniapp", "text": r.MenuButton.MiniApp.Text, "url": r.MenuButton.MiniApp.Url}
			}
		}
		return json.Marshal(out)
	case *wire.MiniAppCustomResponse:
		return json.Marshal(map[string]any{"acknowledged": true, "data": r.Data})
	case *wire.LinkSummaryResponse:
		return json.Marshal(map[string]any{"summary": r.Summary})
	default:
		return json.RawMessage(`{"acknowledged":true}`), nil
	}
}
func auxHTTPURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && len(raw) <= 16384
}
