package balemeow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

func TestHistoryModesUseOfficialEnumsAndDirectionalBoundaries(t *testing.T) {
	for _, mode := range []int32{1, 2, 3} {
		t.Run(map[int32]string{1: "forward", 2: "backward", 3: "both"}[mode], func(t *testing.T) {
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				r := m.Request
				q := &wire.HistoryRequest{}
				if r.Method != "LoadHistory" || decode(r.Payload, q) != nil || q.LoadMode != mode || q.Date != 1720000000000 || q.Limit != 3 {
					t.Errorf("unexpected request: %s %+v", r.Method, q)
				}
				// Non-monotonic provider order must not choose the wrong cursor.
				rows := []*wire.HistoryItem{}
				for i, date := range []int64{1720000000001, 1720000000003, 1719999999999} {
					rows = append(rows, &wire.HistoryItem{SenderId: 42, Rid: int64(i + 1), Date: date, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}})
				}
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.HistoryResponse{History: rows})}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			body, _ := json.Marshal(map[string]any{"peer": map[string]string{"type": "user", "id": "42"}, "date": "1720000000000", "load_mode": mode, "limit": 3})
			raw, err := c.Call(context.Background(), "chat.history", body)
			if err != nil {
				t.Fatal(err)
			}
			var page map[string]json.RawMessage
			if json.Unmarshal(raw, &page) != nil {
				t.Fatal(string(raw))
			}
			if mode == 3 {
				if _, ok := page["next_date"]; ok {
					t.Fatal("both window must not invent a pagination direction")
				}
				if string(page["before_date"]) != `"1719999999999"` || string(page["after_date"]) != `"1720000000003"` {
					t.Fatal(string(raw))
				}
			} else {
				want := `"1719999999999"`
				if mode == 1 {
					want = `"1720000000003"`
				}
				if string(page["next_date"]) != want {
					t.Fatal(string(raw))
				}
			}
		})
	}
}

func TestHistoryModeValidationBeforeNetwork(t *testing.T) {
	c := New(Options{})
	for _, raw := range []string{
		`{"peer":{"type":"user","id":"42"},"load_mode":0}`,
		`{"peer":{"type":"user","id":"42"},"load_mode":4}`,
		`{"peer":{"type":"user","id":"42"},"load_mode":3}`,
	} {
		if _, err := c.history(context.Background(), json.RawMessage(raw)); codeOf(err) != "INVALID_REQUEST" {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}

func TestHistoryForwardDefaultStartsAtEpoch(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		q := &wire.HistoryRequest{}
		if m.Request.Method != "LoadHistory" || decode(m.Request.Payload, q) != nil || q.Date != 0 || q.LoadMode != 1 {
			t.Errorf("wrong initial forward request: %+v", q)
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.HistoryResponse{})}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	raw, err := c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"},"load_mode":1}`))
	if err != nil || string(raw) != `{"messages":[]}` {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestHistoryStopsNonadvancingDirectionalPages(t *testing.T) {
	for _, mode := range []int32{1, 2} {
		for _, equal := range []bool{true, false} {
			name := map[int32]string{1: "forward", 2: "backward"}[mode] + "/wrong_direction"
			if equal {
				name = map[int32]string{1: "forward", 2: "backward"}[mode] + "/equal_timestamp"
			}
			t.Run(name, func(t *testing.T) {
				const anchor = int64(1720000000000)
				date := anchor
				if !equal {
					date--
					if mode == 2 {
						date = anchor + 1
					}
				}
				var fake *fakeWS
				fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
					if m.Request == nil {
						return
					}
					q := &wire.HistoryRequest{}
					if m.Request.Method != "LoadHistory" || decode(m.Request.Payload, q) != nil || q.Date != anchor || q.LoadMode != mode || q.Limit != 2 {
						t.Error("unexpected history query")
						return
					}
					rows := []*wire.HistoryItem{}
					for _, rid := range []int64{11, 12} {
						rows = append(rows, &wire.HistoryItem{SenderId: 42, Rid: rid, Date: date, Message: &wire.Message{Text: &wire.TextMessage{Text: "synthetic"}}})
					}
					fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.HistoryResponse{History: rows})}})
				})
				c := fake.client()
				connectTest(t, c, acceptingSink)
				body, _ := json.Marshal(map[string]any{"peer": map[string]string{"type": "user", "id": "42"}, "date": "1720000000000", "limit": 2, "load_mode": mode})
				raw, err := c.Call(context.Background(), "chat.history", body)
				if err != nil {
					t.Fatal(err)
				}
				var page struct {
					Messages             []historyMessage `json:"messages"`
					Incomplete           bool             `json:"incomplete"`
					PaginationStopReason string           `json:"pagination_stop_reason"`
				}
				if json.Unmarshal(raw, &page) != nil || len(page.Messages) != 2 || !page.Incomplete || page.PaginationStopReason != "non_advancing_cursor" {
					t.Fatalf("missing nonadvancing-page limit: %s", raw)
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				if _, exists := fields["next_date"]; exists {
					t.Fatalf("unsafe/repeating cursor: %s", raw)
				}
				if page.Messages[0].ID != "11" || page.Messages[1].ID != "12" {
					t.Fatal("boundary messages were discarded")
				}
			})
		}
	}
}
