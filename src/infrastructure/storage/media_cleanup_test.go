package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestCleanupRegistrationIncludesDeletedConnectionsAndRejectsConflicts(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "cleanup")
	rel := filepath.ToSlash(filepath.Join(d.ConnectionID, "asset"))
	abs := filepath.Join(t.TempDir(), filepath.FromSlash(rel))
	registered, err := s.MediaFileRegistered(ctx, d.ConnectionID, "asset", rel, abs)
	require.NoError(t, err)
	require.False(t, registered)
	require.NoError(t, s.SaveMedia(ctx, domains.Media{ID: "asset", ConnectionID: d.ConnectionID, Path: rel, Size: 1}))
	require.NoError(t, s.DeleteDevice(ctx, d.ConnectionID))
	registered, err = s.MediaFileRegistered(ctx, d.ConnectionID, "asset", rel, abs)
	require.NoError(t, err)
	require.True(t, registered)
	_, err = s.MediaFileRegistered(ctx, "unknown-connection", "asset", rel, abs)
	require.Error(t, err)
	_, err = s.MediaFileRegistered(ctx, d.ConnectionID, "asset", "different/path", abs)
	require.Error(t, err)
	other := device(t, s, "different")
	_, err = s.MediaFileRegistered(ctx, other.ConnectionID, "asset", rel, abs)
	require.Error(t, err)
}

func TestNestedMediaAdmissionAndSchedulePinTransfer(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "nested-media")
	other := device(t, s, "other-media")
	require.NoError(t, s.SaveMedia(ctx, domains.Media{ID: "shared", ConnectionID: d.ConnectionID, Path: "synthetic/shared", Size: 1}))
	request := domains.SendRequest{Kind: "operation", Operation: "account.avatar", Payload: json.RawMessage(`{"media_id":"shared"}`)}
	_, _, err := s.Enqueue(ctx, other.ConnectionID, request, "foreign", AdmissionLimits{Global: 100})
	errorCode(t, err, "NOT_FOUND")
	_, err = s.CreateSchedule(ctx, other.ConnectionID, request, time.Now().Add(time.Hour))
	errorCode(t, err, "NOT_FOUND")
	// Storage also protects reviewed nested references without the optional
	// top-level convenience copy, independently of the usecase schedule allowlist.
	at := time.Now().Add(time.Hour)
	first, err := s.CreateSchedule(ctx, d.ConnectionID, request, at)
	require.NoError(t, err)
	second, err := s.CreateSchedule(ctx, d.ConnectionID, request, at)
	require.NoError(t, err)
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, "shared"), "MEDIA_IN_USE")
	require.NoError(t, s.SetScheduleState(ctx, d.ConnectionID, first.ID, "cancelled"))
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, "shared"), "MEDIA_IN_USE")
	op, err := s.MaterializeSchedule(ctx, d.ConnectionID, second.ID, second.NextAt, nil, AdmissionLimits{Global: 100})
	require.NoError(t, err)
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, "shared"), "MEDIA_IN_USE")
	claimed, err := s.ClaimOperations(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", ""))
	errorCode(t, s.DeleteMedia(ctx, d.ConnectionID, "shared"), "MEDIA_IN_USE")
	_, err = s.GetMedia(ctx, d.ConnectionID, "shared")
	require.NoError(t, err)
}

func TestMalformedPendingMediaReferencePreventsDeletion(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	d := device(t, s, "malformed-media")
	require.NoError(t, s.SaveMedia(ctx, domains.Media{ID: "shared", ConnectionID: d.ConnectionID, Path: "synthetic/shared", Size: 1}))
	op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("fixture"), "once", AdmissionLimits{Global: 100})
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE operations SET request=? WHERE id=?`, `{"kind":"operation","operation":"account.avatar","payload":{"media_id":123}}`, op.ID)
	require.NoError(t, err)
	require.Error(t, s.DeleteMedia(ctx, d.ConnectionID, "shared"))
	_, err = s.GetMedia(ctx, d.ConnectionID, "shared")
	require.NoError(t, err)
}
