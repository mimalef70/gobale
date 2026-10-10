package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func orderedMedia(provider domains.Provider, id string) domains.ProviderMedia {
	return domains.ProviderMedia{Provider: provider, Version: 1, FileID: id, Name: "synthetic.bin", Size: 12, ContentType: "application/octet-stream", Data: json.RawMessage(`{"private":"synthetic-reference"}`)}
}
func orderedMediaEvent(provider domains.Provider, id, kind string, at int64, media *domains.ProviderMedia, sequence int64) domains.Event {
	e := domains.Event{Provider: provider, ID: id, Type: kind, Peer: domains.Peer{Type: "user", ID: "42"}, MessageID: "123", Time: time.UnixMilli(at), Payload: json.RawMessage(`{"kind":"file"}`), Media: media}
	if media != nil {
		e.Message = &domains.Message{ID: "123", Media: &domains.MessageMedia{FileID: media.FileID, DownloadSupported: true}}
	}
	if sequence > 0 {
		e.MediaRevision = &domains.MediaRevision{Scope: "account", Sequence: sequence}
	}
	return e
}
func acceptMediaEvent(t *testing.T, s *Store, conn string, event domains.Event) {
	t.Helper()
	_, err := s.AppendBatch(context.Background(), conn, domains.EventBatch{Events: []domains.Event{event}}, nil)
	require.NoError(t, err)
}
func requireMediaID(t *testing.T, s *Store, conn, id string) {
	t.Helper()
	m, err := s.GetProviderMedia(context.Background(), conn, domains.Peer{Type: "user", ID: "42"}, "123")
	if id == "" {
		errorCode(t, err, "NOT_FOUND")
		return
	}
	require.NoError(t, err)
	require.Equal(t, id, m.FileID)
}

func TestMediaRevisionSameSecondEditDeleteSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d, err := s.CreateDevice(ctx, "eitaa", domains.ProviderEitaa)
	require.NoError(t, err)
	a, b, c := orderedMedia(d.Provider, "1"), orderedMedia(d.Provider, "2"), orderedMedia(d.Provider, "3")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "original", "message", 1000, &a, 10))
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "edit-one", "message.edited", 1000, &b, 11))
	requireMediaID(t, s, d.ConnectionID, "2")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "edit-two", "message.edited", 1000, &c, 12))
	requireMediaID(t, s, d.ConnectionID, "3")
	// Real sequence wins over a later but unrelated observation timestamp.
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "late-edit", "message.edited", 5000, &b, 11))
	requireMediaID(t, s, d.ConnectionID, "3")
	// A changed duplicate cannot inject ordering evidence into accepted bytes.
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "edit-one", "message.edited", 9000, &a, 99))
	requireMediaID(t, s, d.ConnectionID, "3")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "delete", "message.deleted", 1000, nil, 13))
	requireMediaID(t, s, d.ConnectionID, "")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "late-original", "message", 9000, &a, 0))
	available, err := s.SaveProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123", c)
	require.NoError(t, err)
	require.False(t, available)
	require.NoError(t, s.Close())
	s, err = Open(path, testKey)
	require.NoError(t, err)
	defer s.Close()
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "late-after-restart", "message.edited", 9999, &c, 12))
	requireMediaID(t, s, d.ConnectionID, "")
	events, err := s.ListEvents(ctx, d.ConnectionID, "", 100, 0)
	require.NoError(t, err)
	for _, event := range events {
		require.Nil(t, event.MediaRevision)
		require.Nil(t, event.Media)
	}
}

func TestUnversionedMediaEditsFailClosedWithoutInventedOrdering(t *testing.T) {
	for _, provider := range []domains.Provider{domains.ProviderEitaa, domains.ProviderRubika} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := context.Background()
			s, _ := testStore(t)
			d, err := s.CreateDevice(ctx, "account", provider)
			require.NoError(t, err)
			a, b, c := orderedMedia(provider, "1"), orderedMedia(provider, "2"), orderedMedia(provider, "3")
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "original", "message", 1000, &a, 0))
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "edit-one", "message.edited", 1000, &b, 0))
			requireMediaID(t, s, d.ConnectionID, "2")
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "edit-two", "message.edited", 1000, &c, 0))
			requireMediaID(t, s, d.ConnectionID, "")
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "late-original", "message", 9000, &a, 0))
			available, err := s.SaveProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123", c)
			require.NoError(t, err)
			require.False(t, available)
			events, err := s.ListEvents(ctx, d.ConnectionID, "", 100, 0)
			require.NoError(t, err)
			for _, e := range events {
				if e.ID == scopedEventID(d.ConnectionID, domains.Event{ID: "edit-two"}) {
					require.False(t, e.Message.Media.DownloadSupported)
				}
			}
			// An actual later timestamp still orders unversioned edits; opaque
			// Rubika update_timestamp strings themselves are never interpreted.
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "later-edit", "message.edited", 2000, &c, 0))
			requireMediaID(t, s, d.ConnectionID, "3")
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "delete", "message.deleted", 2000, nil, 0))
			acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(provider, "late-copy", "message.edited", 2000, &b, 0))
			requireMediaID(t, s, d.ConnectionID, "")
		})
	}
}

func TestMediaRevisionScopeAndBatchAtomicity(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d, err := s.CreateDevice(ctx, "account", domains.ProviderEitaa)
	require.NoError(t, err)
	m := orderedMedia(d.Provider, "1")
	for _, revision := range []*domains.MediaRevision{{Scope: "account", Sequence: 0}, {Scope: "account", Sequence: 2147483648}, {Scope: "channel:99", Sequence: 1}, {Scope: "other", Sequence: 1}} {
		e := orderedMediaEvent(d.Provider, "invalid", "message", 1000, &m, 0)
		e.MediaRevision = revision
		_, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{e}, Checkpoints: []domains.CheckpointTransition{{Scope: "account", Next: "1"}}}, nil)
		errorCode(t, err, "INVALID_MEDIA_REVISION")
		cp, err := s.ScopedCheckpoint(ctx, d.ConnectionID, "account")
		require.NoError(t, err)
		require.Empty(t, cp)
	}
	// A classic chat cannot acquire a channel revision; rejection is atomic.
	e := orderedMediaEvent(d.Provider, "group-original", "message", 1000, &m, 1)
	e.Peer.Type = "group"
	acceptMediaEvent(t, s, d.ConnectionID, e)
	newer := orderedMedia(d.Provider, "2")
	e.ID = "other-stream"
	e.Type = "message.edited"
	e.Media = &newer
	e.MediaRevision = &domains.MediaRevision{Scope: "channel:42", Sequence: 9}
	_, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{e}, Checkpoints: []domains.CheckpointTransition{{Scope: "namespace", Next: "changed"}}}, nil)
	errorCode(t, err, "INVALID_MEDIA_REVISION")
	stored, err := s.GetProviderMedia(ctx, d.ConnectionID, e.Peer, e.MessageID)
	require.NoError(t, err)
	require.Equal(t, "1", stored.FileID)
	cp, err := s.ScopedCheckpoint(ctx, d.ConnectionID, "namespace")
	require.NoError(t, err)
	require.Empty(t, cp)
	// A supergroup with the same numeric wire ID owns a distinct media key.
	e.ID = "supergroup"
	e.Peer.ID = "channel_42"
	acceptMediaEvent(t, s, d.ConnectionID, e)
	stored, err = s.GetProviderMedia(ctx, d.ConnectionID, e.Peer, e.MessageID)
	require.NoError(t, err)
	require.Equal(t, "2", stored.FileID)
	e.ID = "wrong-account-scope"
	e.MediaRevision = &domains.MediaRevision{Scope: "account", Sequence: 10}
	_, err = s.AppendBatch(ctx, d.ConnectionID, domains.EventBatch{Events: []domains.Event{e}}, nil)
	errorCode(t, err, "INVALID_MEDIA_REVISION")
}

func TestMediaRevisionRetainsTimestampWatermarkWithoutChangingPublicTime(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d, err := s.CreateDevice(ctx, "account", domains.ProviderEitaa)
	require.NoError(t, err)
	a, b := orderedMedia(d.Provider, "1"), orderedMedia(d.Provider, "2")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "first", "message.edited", 2000, &a, 1))
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "newer-pts", "message.edited", 1000, &b, 2))
	requireMediaID(t, s, d.ConnectionID, "2")
	acceptMediaEvent(t, s, d.ConnectionID, orderedMediaEvent(d.Provider, "unversioned-stale", "message.edited", 1500, &a, 0))
	requireMediaID(t, s, d.ConnectionID, "2")
	var watermark int64
	err = s.db.QueryRow(`SELECT event_time FROM provider_media WHERE connection_id=? AND peer_key='user:42' AND message_id='123'`, d.ConnectionID).Scan(&watermark)
	require.NoError(t, err)
	require.Equal(t, int64(2000), watermark)
	events, err := s.ListEvents(ctx, d.ConnectionID, "", 100, 0)
	require.NoError(t, err)
	for _, e := range events {
		if e.ID == scopedEventID(d.ConnectionID, domains.Event{ID: "newer-pts"}) {
			require.Equal(t, int64(1000), e.Time.UnixMilli())
		}
	}
}

func TestMigratedMediaKeepsTimestampFallbackAndEqualTimeConflictIsUnavailable(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "bale")
	a := privateMedia()
	original := documentEvent("original", 1000, &a)
	_, err := s.AppendEvent(ctx, d.ConnectionID, original, nil)
	require.NoError(t, err)
	// An absent order row is exactly the migrated schema-7 representation.
	_, err = s.db.Exec(`DELETE FROM provider_media_order WHERE connection_id=?`, d.ConnectionID)
	require.NoError(t, err)
	b := a
	b.FileID = "888"
	edit := documentEvent("edit", 1000, &b)
	edit.Type = "message.edited"
	_, err = s.AppendEvent(ctx, d.ConnectionID, edit, nil)
	require.NoError(t, err)
	requireMediaID(t, s, d.ConnectionID, "")
	edit.ID = "later"
	edit.Time = time.UnixMilli(2000)
	_, err = s.AppendEvent(ctx, d.ConnectionID, edit, nil)
	require.NoError(t, err)
	requireMediaID(t, s, d.ConnectionID, "888")
}
