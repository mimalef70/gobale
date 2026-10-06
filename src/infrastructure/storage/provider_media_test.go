package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func privateMedia() domains.ProviderMedia {
	return domains.ProviderMedia{FileID: "777", AccessHash: "-872345678901234567", Size: 1234, Name: "sample.jpg", ContentType: "image/jpeg"}
}
func documentEvent(id string, at int64, m *domains.ProviderMedia) domains.Event {
	return domains.Event{ID: id, Type: "message", Peer: domains.Peer{Type: "user", ID: "42"}, MessageID: "123", Time: time.UnixMilli(at).UTC(), Payload: json.RawMessage(`{"kind":"document","file_id":"777"}`), Checkpoint: "cp-" + id, Media: m}
}
func TestProviderMediaIsScopedEncryptedAndPrivate(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	a := device(t, s, "alpha")
	b := device(t, s, "beta")
	m := privateMedia()
	event := documentEvent("first", 1720000000000, &m)
	inserted, e := s.AppendEvent(ctx, a.ConnectionID, event, []WebhookTarget{{URL: "https://example.test"}})
	require.NoError(t, e)
	require.True(t, inserted)
	got, e := s.GetProviderMedia(ctx, a.ConnectionID, event.Peer, event.MessageID)
	require.NoError(t, e)
	require.Equal(t, m, got)
	_, e = s.GetProviderMedia(ctx, b.ConnectionID, event.Peer, event.MessageID)
	errorCode(t, e, "NOT_FOUND")
	_, e = s.GetProviderMedia(ctx, a.ConnectionID, domains.Peer{Type: "group", ID: "42"}, event.MessageID)
	errorCode(t, e, "NOT_FOUND")
	_, e = s.GetProviderMedia(ctx, a.ConnectionID, event.Peer, "124")
	errorCode(t, e, "NOT_FOUND")
	public, e := json.Marshal(got)
	require.NoError(t, e)
	require.NotContains(t, string(public), m.AccessHash)
	events, e := s.ListEvents(ctx, a.ConnectionID, "", 50, 0)
	require.NoError(t, e)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Media)
	delivery, e := s.ListDeliveries(ctx, a.ConnectionID, 50, 0)
	require.NoError(t, e)
	require.NotContains(t, string(delivery[0].Body), m.AccessHash)
	require.NotContains(t, string(delivery[0].Body), "access_hash")
	require.NoError(t, s.Close())
	disk, e := os.ReadFile(p)
	require.NoError(t, e)
	require.NotContains(t, string(disk), m.AccessHash)
	reopened, e := Open(p, testKey)
	require.NoError(t, e)
	defer reopened.Close()
	got, e = reopened.GetProviderMedia(ctx, a.ConnectionID, event.Peer, event.MessageID)
	require.NoError(t, e)
	require.Equal(t, m, got)
	require.NoError(t, reopened.DeleteDevice(ctx, a.ConnectionID))
	replacement := device(t, reopened, "alpha")
	_, e = reopened.GetProviderMedia(ctx, replacement.ConnectionID, event.Peer, event.MessageID)
	errorCode(t, e, "NOT_FOUND")
}
func TestProviderMediaReferenceEditDeleteAndLateHistory(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	original := privateMedia()
	event := documentEvent("original", 1000, &original)
	_, e := s.AppendEvent(ctx, d.ConnectionID, event, nil)
	require.NoError(t, e)
	newer := original
	newer.FileID = "888"
	newer.AccessHash = "987654321"
	edited := documentEvent("edited", 2000, &newer)
	edited.Type = "message.edited"
	_, e = s.AppendEvent(ctx, d.ConnectionID, edited, nil)
	require.NoError(t, e)
	got, e := s.GetProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID)
	require.NoError(t, e)
	require.Equal(t, newer, got)
	late := documentEvent("late-original-copy", 1000, &original)
	_, e = s.AppendEvent(ctx, d.ConnectionID, late, nil)
	require.NoError(t, e)
	require.NoError(t, s.SaveProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID, original))
	got, e = s.GetProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID)
	require.NoError(t, e)
	require.Equal(t, newer, got)
	deleted := documentEvent("deleted", 3000, nil)
	deleted.Type = "message.deleted"
	_, e = s.AppendEvent(ctx, d.ConnectionID, deleted, nil)
	require.NoError(t, e)
	_, e = s.GetProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID)
	errorCode(t, e, "NOT_FOUND")
	require.NoError(t, s.SaveProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID, original))
	_, e = s.GetProviderMedia(ctx, d.ConnectionID, event.Peer, event.MessageID)
	errorCode(t, e, "NOT_FOUND")
	// A text-only edit removes the former attachment too.
	edited.MessageID = "456"
	edited.ID = "other-doc"
	_, e = s.AppendEvent(ctx, d.ConnectionID, edited, nil)
	require.NoError(t, e)
	edited.ID = "other-text"
	edited.Media = nil
	edited.Time = time.UnixMilli(4000)
	_, e = s.AppendEvent(ctx, d.ConnectionID, edited, nil)
	require.NoError(t, e)
	_, e = s.GetProviderMedia(ctx, d.ConnectionID, event.Peer, "456")
	errorCode(t, e, "NOT_FOUND")
}
func TestProviderMediaPersistenceIsAtomicWithEventAndCheckpoint(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	m := privateMedia()
	m.AccessHash = "not an integer"
	_, e := s.AppendEvent(ctx, d.ConnectionID, documentEvent("invalid", 1000, &m), nil)
	errorCode(t, e, "INVALID_PROVIDER_MEDIA")
	count, e := s.EventCount(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Zero(t, count)
	cp, e := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, e)
	require.Empty(t, cp)
	m = privateMedia()
	_, e = s.AppendEvent(ctx, d.ConnectionID, documentEvent("delivery-failure", 1000, &m), []WebhookTarget{{URL: "invalid"}})
	errorCode(t, e, "INVALID_WEBHOOK_URL")
	_, e = s.GetProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123")
	errorCode(t, e, "NOT_FOUND")
}
func TestProviderMediaMigrationAndBackupRestore(t *testing.T) {
	s, p := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	// Simulate the previous released schema without modifying account/session data.
	_, e := s.db.Exec(`DROP TABLE provider_media`)
	require.NoError(t, e)
	_, e = s.db.Exec(`DROP TABLE schedule_idempotency`)
	require.NoError(t, e)
	restoreLegacyDeliveryOrder(t, s)
	_, e = s.db.Exec(`DROP INDEX deliveries_pending_order`)
	require.NoError(t, e)
	_, e = s.db.Exec(`DROP INDEX deliveries_inflight_target`)
	require.NoError(t, e)
	_, e = s.db.Exec(`UPDATE gobale_meta SET version=1`)
	require.NoError(t, e)
	require.NoError(t, s.Close())
	upgraded, e := Open(p, testKey)
	require.NoError(t, e)
	defer upgraded.Close()
	var version int
	require.NoError(t, upgraded.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, schemaVersion, version)
	m := privateMedia()
	require.NoError(t, upgraded.SaveProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123", m))
	backup := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, upgraded.Backup(ctx, backup))
	restored, e := Open(backup, testKey)
	require.NoError(t, e)
	defer restored.Close()
	got, e := restored.GetProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123")
	require.NoError(t, e)
	require.Equal(t, m, got)
}

func TestProviderMediaPreservesNegativeSignedFileID(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	m := privateMedia()
	m.FileID = "-8123456789012345678"
	e := s.SaveProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123", m)
	require.NoError(t, e)
	got, e := s.GetProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "123")
	require.NoError(t, e)
	require.Equal(t, m, got)
	m.FileID = "0"
	errorCode(t, s.SaveProviderMedia(ctx, d.ConnectionID, domains.Peer{Type: "user", ID: "42"}, "124", m), "INVALID_PROVIDER_MEDIA")
}

func TestProviderMediaNegativeMessageIDLookupAndTombstone(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "alpha")
	m := privateMedia()
	peer := domains.Peer{Type: "user", ID: "42"}
	messageID := "-8123456789012345678"
	require.NoError(t, s.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, m))
	got, err := s.GetProviderMedia(ctx, d.ConnectionID, peer, messageID)
	require.NoError(t, err)
	require.Equal(t, m, got)
	e := documentEvent("deleted-negative-message", 1720000000000, nil)
	e.Type = "message.deleted"
	e.MessageID = messageID
	_, err = s.AppendEvent(ctx, d.ConnectionID, e, nil)
	require.NoError(t, err)
	_, err = s.GetProviderMedia(ctx, d.ConnectionID, peer, messageID)
	errorCode(t, err, "NOT_FOUND")
	require.NoError(t, s.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, m))
	_, err = s.GetProviderMedia(ctx, d.ConnectionID, peer, messageID)
	errorCode(t, err, "NOT_FOUND")
	for _, invalid := range []string{"0", "+123", "-9223372036854775809", "9223372036854775808"} {
		errorCode(t, s.SaveProviderMedia(ctx, d.ConnectionID, peer, invalid, m), "INVALID_MESSAGE_ID")
	}
}
