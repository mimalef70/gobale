package storage

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPartialEditDurabilitySearchAndIsolation(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := providerDevice(t, s, "rubika", domains.ProviderRubika)
	other := providerDevice(t, s, "other", domains.ProviderRubika)
	targets := []WebhookTarget{{URL: "https://synthetic.invalid/hooks", Secret: "synthetic"}}
	for i, body := range []*string{nil, new(""), new("سلام %_🙂")} {
		event := domains.Event{Provider: domains.ProviderRubika, ID: []string{"absent", "empty", "text"}[i], Type: "message.edited", Peer: domains.Peer{Type: "user", ID: "u0peer"}, MessageID: "42", Direction: "unknown", Time: time.Unix(int64(100+i), 0).UTC(), Payload: json.RawMessage(`{"partial":true,"timestamp_source":"observed","provider_update_timestamp":"opaque","unprojected_fields":true}`), MessagePatch: &domains.MessagePatch{ID: "42", ChatID: "u0peer", OriginalMessageID: "42", Partial: true, Body: body, Supported: body != nil}}
		_, err := s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{event}}, targets)
		require.NoError(t, err)
	}
	rows, err := s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{Search: "سلام %_🙂"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Message)
	require.Equal(t, "سلام %_🙂", *rows[0].MessagePatch.Body)
	rows, err = s.ListEventsFiltered(ctx, other.ConnectionID, domains.EventFilter{Search: "سلام %_🙂"})
	require.NoError(t, err)
	require.Empty(t, rows)
	before, err := s.ListDeliveries(ctx, d.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Len(t, before, 3)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	defer s.Close()
	after, err := s.ListDeliveries(ctx, d.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Len(t, after, 3)
	for i := range before {
		require.Equal(t, before[i].Body, after[i].Body)
		require.Equal(t, before[i].EventID, after[i].EventID)
	}
	events, err := s.ListEvents(ctx, d.ConnectionID, "", 100, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for _, e := range events {
		require.Nil(t, e.Message)
		require.NotNil(t, e.MessagePatch)
		require.True(t, e.MessagePatch.Partial)
		require.Empty(t, e.SenderID)
		require.Equal(t, "unknown", e.Direction)
	}
}

func TestFallbackEventIdentityIncludesPatchButPreservesHistoricalShape(t *testing.T) {
	event := domains.Event{Type: "message.edited", Peer: domains.Peer{Type: "user", ID: "u0peer"}, MessageID: "42", Payload: json.RawMessage(`{}`), MessagePatch: &domains.MessagePatch{ID: "42", ChatID: "u0peer", OriginalMessageID: "42", Partial: true, Body: new("first"), Supported: true}}
	first := scopedEventID("connection", event)
	event.MessagePatch.Body = new("second")
	require.NotEqual(t, first, scopedEventID("connection", event))
	event.MessagePatch = nil
	// A literal pre-patch fallback identity must retain its accepted hash.
	require.Equal(t, "f99db48ec08c85e346d4acd97ac20628b4b722ca5075708189ccdf64b872fc16", scopedEventID("connection", event))
}
