package balemeow

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func TestReferenceTextGolden(t *testing.T) {
	// Synthetic fixture: peer=user 77, caller-persisted RID=99, text=hi.
	// Tags are transcribed from the reviewed wire layout, independently of
	// any generated catalog. Unknown fields survive decoding for forward safety.
	fixture, _ := hex.DecodeString("0a040801104d10631a067a040a026869")
	msg := &wire.SendMessageRequest{}
	if err := decode(fixture, msg); err != nil {
		t.Fatal(err)
	}
	if msg.Peer.Id != 77 || msg.Peer.Type != 1 || msg.Rid != 99 || msg.Message.Text.Text != "hi" {
		t.Fatalf("bad golden decode: %v", msg)
	}
	actual, err := proto.Marshal(msg)
	if err != nil || hex.EncodeToString(actual) != hex.EncodeToString(fixture) {
		t.Fatal("golden roundtrip failed")
	}
}
func TestGRPCStrictFrames(t *testing.T) {
	good := append(grpcFrame(0, []byte{1, 2}), grpcFrame(128, []byte("grpc-status: 0\r\n"))...)
	tests := []struct {
		name   string
		data   []byte
		status string
		code   string
	}{
		{"good", good, "", ""},
		{"truncated header", []byte{0, 0}, "", "PROTOCOL_ERROR"},
		{"truncated payload", []byte{0, 0, 0, 0, 9, 1}, "", "PROTOCOL_ERROR"},
		{"compressed", grpcFrame(1, []byte{1}), "0", "PROTOCOL_ERROR"},
		{"missing status", grpcFrame(0, []byte{1}), "", "PROTOCOL_ERROR"},
		{"duplicate unary", append(grpcFrame(0, nil), grpcFrame(0, nil)...), "0", "PROTOCOL_ERROR"},
		{"rate limit", grpcFrame(128, []byte("grpc-status: 8\r\ngrpc-message: contains-private-token\r\n")), "", "PROVIDER_RATE_LIMITED"},
		{"data after trailer", append(grpcFrame(128, []byte("grpc-status: 0\r\n")), grpcFrame(0, nil)...), "", "PROTOCOL_ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGRPCWeb(tc.data, tc.status, "")
			if codeOf(err) != tc.code {
				t.Fatalf("got %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-token") {
				t.Fatal("provider text leaked")
			}
		})
	}
}
func TestEventIdentityAndDirection(t *testing.T) {
	data := marshal(t, &wire.UpdateContainer{Message: &wire.UpdateMessage{Peer: &wire.Peer{Type: 1, Id: 77}, SenderId: 12345, Date: 1720000000000, Rid: 9007199254740993, Message: &wire.Message{Text: &wire.TextMessage{Text: "hello"}}}})
	first, err := decodeEvents("12345", data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := decodeEvents("12345", data)
	if err != nil {
		t.Fatal(err)
	}
	other, err := decodeEvents("77", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != second[0].ID || first[0].ID == other[0].ID {
		t.Fatal("unstable/account-independent event ID")
	}
	if first[0].Direction != "outgoing" || other[0].Direction != "incoming" || first[0].MessageID != "9007199254740993" || first[0].Checkpoint != "" {
		t.Fatal("bad event semantics")
	}
}
func TestUnknownUpdateDoesNotExposeOpaqueCredentials(t *testing.T) {
	fixture := []byte{0x0a, 0x05, 't', 'o', 'k', 'e', 'n'}
	events, err := decodeEvents("123", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "protocol.unsupported_update" || strings.Contains(string(events[0].Payload), "token") {
		t.Fatal("unknown update leaked opaque input")
	}
}
func TestIDValidation(t *testing.T) {
	for _, bad := range []string{"", "0", "-1", "9223372036854775808", " 1", "1.0", "+1"} {
		if _, err := positiveID(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if id, err := positiveID("9007199254740993"); err != nil || id != 9007199254740993 {
		t.Fatal("lost int64 precision")
	}
}
func FuzzGRPCWeb(f *testing.F) {
	f.Add(append(grpcFrame(0, []byte{8, 1}), grpcFrame(128, []byte("grpc-status: 0\r\n"))...))
	f.Add([]byte{0, 255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseGRPCWeb(data, "", "")
	})
}
func FuzzServerFrames(f *testing.F) {
	f.Add([]byte{0x2a, 0x02, 0x08, 0x01})
	f.Add([]byte{0x0a, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_ = decode(data, &wire.ServerMessage{})
		_, _ = decodeEvents("123", data)
	})
}
