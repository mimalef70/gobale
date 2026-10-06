package balemeow

import (
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"time"
)

func groupEvents(account string, data []byte) ([]domains.Event, error) {
	u := &wire.GroupUpdateUnion{}
	if decode(data, u) != nil {
		return nil, protocolError()
	}
	out := []domains.Event{}
	add := func(kind string, id uint32, b map[string]any) error {
		if id == 0 {
			return protocolError()
		}
		p := domains.Peer{Type: "group", ID: uid(id)}
		raw, _ := json.Marshal(b)
		out = append(out, domains.Event{ID: eventHash(account + "|" + kind + "|" + p.Key() + "|" + string(data)), Type: kind, AccountID: account, Peer: p, Direction: "unknown", Time: time.Now().UTC(), Payload: raw})
		return nil
	}
	for _, x := range []struct {
		k string
		v *wire.GroupIDUpdate
	}{{"group.avatar_changed", u.Avatar}, {"group.members_became_async", u.AsyncMembers}, {"group.history_shared", u.HistoryShared}, {"group.orphaned", u.Orphaned}} {
		if v := x.v; v != nil {
			if e := add(x.k, v.GroupId, map[string]any{}); e != nil {
				return nil, e
			}
		}
	}
	for _, x := range []struct {
		k, key string
		v      *wire.GroupStringUpdate
	}{{"group.title_changed", "title", u.Title}, {"group.username_changed", "username", u.Username}} {
		if v := x.v; v != nil {
			if e := add(x.k, v.GroupId, map[string]any{x.key: v.Value}); e != nil {
				return nil, e
			}
		}
	}
	for _, x := range []struct {
		k, key string
		v      *wire.GroupTextUpdate
	}{{"group.topic_changed", "topic", u.Topic}, {"group.about_changed", "about", u.About}} {
		if v := x.v; v != nil {
			if e := add(x.k, v.GroupId, map[string]any{x.key: v.GetValue().GetValue()}); e != nil {
				return nil, e
			}
		}
	}
	for _, x := range []struct {
		k, key string
		v      *wire.GroupNumberUpdate
	}{{"group.restriction_changed", "restriction", u.Restriction}, {"group.members_count_changed", "members_count", u.Count}} {
		if v := x.v; v != nil {
			if v.Value < 0 {
				return nil, protocolError()
			}
			if e := add(x.k, v.GroupId, map[string]any{x.key: v.Value}); e != nil {
				return nil, e
			}
		}
	}
	for _, x := range []struct {
		k, key string
		v      *wire.GroupBoolUpdate
	}{{"group.membership_changed", "is_member", u.Member}, {"group.can_send_changed", "can_send_messages", u.CanSend}, {"group.can_view_members_changed", "can_view_members", u.CanViewMembers}, {"group.can_invite_changed", "can_invite_members", u.CanInvite}} {
		if v := x.v; v != nil {
			if e := add(x.k, v.GroupId, map[string]any{x.key: v.Value}); e != nil {
				return nil, e
			}
		}
	}
	if v := u.Owner; v != nil {
		if v.UserId == 0 {
			return nil, protocolError()
		}
		if e := add("group.owner_changed", v.GroupId, map[string]any{"user_id": uid(v.UserId)}); e != nil {
			return nil, e
		}
	}
	if v := u.Admin; v != nil {
		if v.UserId == 0 {
			return nil, protocolError()
		}
		if e := add("group.admin_changed", v.GroupId, map[string]any{"user_id": uid(v.UserId), "is_admin": v.IsAdmin}); e != nil {
			return nil, e
		}
	}
	if v := u.Members; v != nil {
		if len(v.Members) > 4096 {
			return nil, protocolError()
		}
		members := []map[string]any{}
		for _, m := range v.Members {
			if m == nil || m.Uid == 0 || m.Date < 0 {
				return nil, protocolError()
			}
			b := map[string]any{"user_id": uid(m.Uid), "inviter_id": uid(m.InviterUid), "date": sid(m.Date)}
			if m.IsAdmin != nil {
				b["is_admin"] = m.IsAdmin.Value
			}
			if m.Title != nil {
				b["title"] = m.Title.Value
			}
			if m.Permissions != nil {
				b["permissions"] = permissionsJSON(m.Permissions)
			}
			members = append(members, b)
		}
		if e := add("group.members_changed", v.GroupId, map[string]any{"members": members}); e != nil {
			return nil, e
		}
	}
	if v := u.MemberPermissions; v != nil {
		if v.UserId == 0 || v.Permissions == nil {
			return nil, protocolError()
		}
		if e := add("group.member_permissions_changed", v.GroupId, map[string]any{"user_id": uid(v.UserId), "permissions": permissionsJSON(v.Permissions)}); e != nil {
			return nil, e
		}
	}
	if v := u.DefaultPermissions; v != nil {
		if v.Group == nil || v.Group.Id == 0 || v.Permissions == nil {
			return nil, protocolError()
		}
		if e := add("group.default_permissions_changed", v.Group.Id, map[string]any{"permissions": permissionsJSON(v.Permissions)}); e != nil {
			return nil, e
		}
	}
	return out, nil
}
