package usecase

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestGenericMutationIsJournaledBeforeCallAndIdempotent(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "groups")
	payload := json.RawMessage(`{"title":"Team", "users":[]}`)
	operation, err := s.Mutate(ctx, d.ID, "group.create", payload, "create-one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Mutate(ctx, d.ID, "group.create", json.RawMessage(`{"users":[],"title":"Team"}`), "create-one")
	if err != nil || again.ID != operation.ID {
		t.Fatal(again, err)
	}
	_, err = s.Mutate(ctx, d.ID, "group.create", json.RawMessage(`{"title":"Other"}`), "create-one")
	codeIs(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = s.Call(ctx, d.ID, "group.create", payload)
	codeIs(t, err, "DURABLE_OPERATION_REQUIRED")
	f.callFn = func(_ context.Context, name string, body json.RawMessage) (json.RawMessage, error) {
		if name != "group.create" {
			t.Error(name)
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		persisted, err := st.GetOperation(ctx, d.ConnectionID, operation.ID)
		if err != nil || persisted.State != "sending" || request["request_id"] != persisted.Request.RequestID || !bale.ValidMessageID(persisted.Request.RequestID) {
			t.Fatalf("call was not durably identified: %+v %s %v", persisted, body, err)
		}
		return json.RawMessage(`{"peer":{"type":"group","id":"78"},"title":"Team","not_added_user_ids":[]}`), nil
	}
	s.processOperation(claimOne(t, st))
	saved, err := s.GetOperation(ctx, d.ID, operation.ID)
	if err != nil || saved.State != "succeeded" || saved.Result == nil || !json.Valid(saved.Result.Data) {
		t.Fatalf("mutation result %+v %v", saved, err)
	}
	if f.sendCalls != 0 || f.callCalls != 1 {
		t.Fatalf("wrong provider dispatch send=%d call=%d", f.sendCalls, f.callCalls)
	}
}
func TestGenericMutationUnknownNeverRetries(t *testing.T) {
	ctx := context.Background()
	f := &fakeClient{status: readyStatus(), callErr: &domains.Error{Code: "SEND_UNKNOWN", Message: "secret", Ambiguous: true, HTTP: 202}}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "groups")
	op, err := s.Mutate(ctx, d.ID, "group.title", json.RawMessage(`{"peer":{"type":"group","id":"78"},"title":"Updated"}`), "title-one")
	if err != nil {
		t.Fatal(err)
	}
	s.processOperation(claimOne(t, st))
	saved, err := s.GetOperation(ctx, d.ID, op.ID)
	if err != nil || saved.State != "unknown" {
		t.Fatal(saved, err)
	}
	next, err := st.ClaimOperations(ctx, 10)
	if err != nil || len(next) != 0 {
		t.Fatal("unknown mutation was retried", next, err)
	}
}
func TestMutationValidationBlocksRawRPCAndSendBypass(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{status: readyStatus()} })
	d := mustDevice(t, s, "groups")
	cases := []struct{ operation, body, key, code string }{
		{"group.create", `{"title":"Team"}`, "", "IDEMPOTENCY_KEY_REQUIRED"},
		{"group.create", `{"title":"Team","request_id":"1"}`, "one", "INVALID_REQUEST"},
		{"group.create", `{"title":"Team","token":"private"}`, "one", "INVALID_REQUEST"},
		{"group.invite", `{"peer":{"type":"group","id":"78"},"users":[{"type":"user","id":"42"}]}`, "invite", ""},
		{"group.description", `{"peer":{"type":"group","id":"78"},"description":""}`, "description-clear", ""},
		{"group.description", `{"peer":{"type":"group","id":"78"}}`, "description-missing", "INVALID_REQUEST"},
		{"group.remove", `{"peer":{"type":"group","id":"78"},"user":{"type":"user","id":"42"}}`, "remove-user", ""},
		{"group.remove", `{"peer":{"type":"group","id":"78"},"users":[{"type":"user","id":"42"}]}`, "remove-list", "INVALID_REQUEST"},
		{"group.invite", `{"peer":{"type":"group","id":"78"},"users":[{"type":"user","id":"42"},{"type":"user","id":"42"}]}`, "one", "INVALID_REQUEST"},
		{"message.edit", `{"peer":{"type":"user","id":"42"},"message_id":"7","message":"updated"}`, "one", ""},
		{"message.edit", `{"peer":{"type":"user","id":"42"},"message_id":"-9007199254740995","message":"updated"}`, "negative-edit", ""},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"-9223372036854775808","just_mine":true}`, "negative-delete", ""},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"0","just_mine":true}`, "zero-delete", "INVALID_REQUEST"},
		{"message.read", `{"peer":{"type":"user","id":"42"},"message_id":"7","date":"1700000000000"}`, "two", ""},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"7"}`, "delete", "INVALID_REQUEST"},
		{"message.delete", `{"peer":{"type":"user","id":"42"},"message_id":"7","just_mine":false}`, "delete", ""},
		{"message.forward", `{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"7"}`, "missing-date", "INVALID_REQUEST"},
		{"message.forward", `{"peer":{"type":"user","id":"43"},"source_peer":{"type":"user","id":"42"},"message_id":"7","source_date":"1720000000000"}`, "forward", ""},
		{"banking.transfer", `{}`, "one", "FEATURE_NOT_SUPPORTED"},
	}
	for _, test := range cases {
		_, err := s.Mutate(ctx, d.ID, test.operation, json.RawMessage(test.body), test.key)
		if test.code == "" {
			if err != nil {
				t.Fatal(test, err)
			}
		} else {
			codeIs(t, err, test.code)
		}
	}
	request := sendReq("hidden operation")
	request.Operation = "group.create"
	request.Payload = json.RawMessage(`{"title":"Team"}`)
	_, err := s.Send(ctx, d.ID, request, "bypass")
	codeIs(t, err, "INVALID_REQUEST")
}
