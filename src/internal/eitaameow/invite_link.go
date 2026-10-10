package eitaameow

import "context"

// An existing invitation is disclosed only through the explicitly requested
// link operation, never through generic chat/profile projections.
func (c *Client) existingGroupInvite(ctx context.Context, peer object) (object, error) {
	method, params, kind, id := "messages.getFullChat", object{"chat_id": peer.num("chat_id")}, "chatFull", peer.num("chat_id")
	if peer.str("_") == "inputPeerChannel" {
		method, params, kind, id = "channels.getFullChannel", object{"channel": inputChannel(peer)}, "channelFull", peer.num("channel_id")
	}
	response, err := c.invoke(ctx, method, params, false, false)
	if err != nil {
		return nil, err
	}
	full := asObject(response["full_chat"])
	if response.str("_") != "messages.chatFull" || full.str("_") != kind || full.num("id") != id {
		return nil, protocolError()
	}
	if err = c.rememberEntities(ctx, response); err != nil {
		return nil, err
	}
	invite := asObject(full["exported_invite"])
	if invite == nil || invite.str("_") == "chatInviteEmptyLayer122" || invite["revoked"] == true {
		return nil, nil
	}
	if (invite.str("_") != "chatInviteExported" && invite.str("_") != "chatInviteExportedLayer122") || !validInviteLink(invite.str("link")) {
		return nil, protocolError()
	}
	return object{"link": invite.str("link")}, nil
}
