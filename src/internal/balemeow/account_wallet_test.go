package balemeow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestWalletReadsPreservePrecisionAndOmitFinancialCredentials(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		r := m.Request
		var result []byte
		switch r.Method {
		case "GetMyWallets":
			if r.Service != "bale.wallet.v1.Wallet" || len(r.Payload) != 0 {
				t.Error("wrong wallet request")
			}
			v := &wire.AccountWallet{Id: "wallet-42", Balances: []*wire.AccountWalletBalance{{Currency: 0, Amount: 9007199254740993}, {Currency: 1, Amount: -1}}, IsActive: &wire.BoolValue{Value: false}}
			// Unknown field3 contains a credential-bearing provider URL. It must
			// never be copied into the gateway JSON or any outgoing request.
			secret := protowire.AppendTag(nil, 3, protowire.BytesType)
			secret = protowire.AppendBytes(secret, marshal(t, &wire.StringValue{Value: "https://wallet.invalid/?token=secret-wallet-link"}))
			v.ProtoReflect().SetUnknown(secret)
			result = marshal(t, &wire.AccountWalletsResponse{Wallets: []*wire.AccountWallet{v}})
		case "GetMyKifpools":
			q := &wire.AccountKifpoolsRequest{}
			if r.Service != "bale.kifpool.v1.Kifpool" || decode(r.Payload, q) != nil || q.PocketType != 2200 || q.InvocationSpot != nil {
				t.Error("wrong kifpool request")
			}
			v := &wire.AccountKifpool{IsMerchant: true, App: "bale", Balance: 9007199254740993, Level: 2}
			secret := []byte{}
			for _, field := range []protowire.Number{4, 6, 7} {
				secret = protowire.AppendTag(secret, field, protowire.BytesType)
				secret = protowire.AppendString(secret, "secret-financial-value")
			}
			v.ProtoReflect().SetUnknown(secret)
			result = marshal(t, &wire.AccountKifpoolsResponse{Wallets: []*wire.AccountKifpool{v}})
		default:
			t.Error("unexpected wallet method")
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: result}})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	for _, tc := range []struct{ op, body, want string }{
		{"wallet.list", `{}`, `"active":false`},
		{"wallet.kifpools", `{"pocket_type":2200}`, `"merchant":true`},
	} {
		raw, err := c.accountWalletCall(context.Background(), tc.op, json.RawMessage(tc.body))
		if err != nil || !strings.Contains(string(raw), `"9007199254740993"`) || !strings.Contains(string(raw), tc.want) {
			t.Fatalf("%s %v", raw, err)
		}
		for _, forbidden := range []string{"secret-", "token", "walletLink", "account", "pan"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("financial credential leaked")
			}
		}
	}
}

func TestWalletReadValidationAndBounds(t *testing.T) {
	c := New(Options{})
	for _, tc := range []struct{ op, body string }{
		{"wallet.list", `{"pocket_type":0}`},
		{"wallet.kifpools", `{"pocket_type":12}`},
		{"wallet.kifpools", `{"token":"private"}`},
		{"wallet.kifpools", `{"pocket_type":-1}`},
	} {
		if _, err := c.accountWalletCall(context.Background(), tc.op, json.RawMessage(tc.body)); codeOf(err) != "INVALID_REQUEST" {
			t.Fatalf("%s %v", tc.body, err)
		}
	}
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		wallets := make([]*wire.AccountWallet, 101)
		for i := range wallets {
			wallets[i] = &wire.AccountWallet{Id: "w"}
		}
		fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Payload: marshal(t, &wire.AccountWalletsResponse{Wallets: wallets})}})
	})
	c = fake.client()
	connectTest(t, c, acceptingSink)
	if _, err := c.accountWalletCall(context.Background(), "wallet.list", json.RawMessage(`{}`)); codeOf(err) != "PROTOCOL_ERROR" {
		t.Fatal(err)
	}
}
