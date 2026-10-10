package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

// Reconstruct the actual schema-7 private layout for migration fixtures. This
// helper is deliberately test-only; production rejects partial schemas.
func restoreV7ProviderSchema(t *testing.T, s *Store) {
	t.Helper()
	tx, err := s.db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	for _, table := range []string{"sessions", "provider_media"} {
		var exists int
		require.NoError(t, tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists))
		if exists == 0 {
			continue
		}
		query := `SELECT rowid,connection_id,cipher FROM sessions`
		if table == "provider_media" {
			query = `SELECT rowid,connection_id,peer_key,message_id,cipher FROM provider_media WHERE cipher IS NOT NULL`
		}
		rows, err := tx.Query(query)
		require.NoError(t, err)
		type record struct {
			row                 int64
			conn, peer, message string
			cipher              []byte
		}
		var records []record
		for rows.Next() {
			var r record
			if table == "sessions" {
				err = rows.Scan(&r.row, &r.conn, &r.cipher)
			} else {
				err = rows.Scan(&r.row, &r.conn, &r.peer, &r.message, &r.cipher)
			}
			require.NoError(t, err)
			records = append(records, r)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		for _, r := range records {
			aad := r.conn + ":session"
			if table == "provider_media" {
				aad = r.conn + ":provider-media:" + r.peer + ":" + r.message
			}
			envelope, err := s.decodePrivate(r.cipher, aad, domains.ProviderBale)
			require.NoError(t, err)
			plain := envelope.Payload
			if table == "sessions" {
				var old map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(plain, &old))
				delete(old, "provider")
				delete(old, "version")
				plain, err = json.Marshal(old)
				require.NoError(t, err)
			}
			cipher, err := s.encrypt(plain, aad)
			require.NoError(t, err)
			_, err = tx.Exec(`UPDATE `+table+` SET cipher=? WHERE rowid=?`, cipher, r.row)
			require.NoError(t, err)
		}
	}
	for _, ddl := range []string{
		`DROP TRIGGER devices_immutable_provider`,
		`DROP TABLE provider_checkpoints`,
		`DROP TABLE connection_dispatch`,
		`DROP TABLE operation_stages`,
		`DROP TABLE provider_media_order`,
		`ALTER TABLE devices DROP COLUMN provider`,
		`ALTER TABLE device_provisioning DROP COLUMN hash_version`,
	} {
		_, err := tx.Exec(ddl)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func TestProviderMigrationPreservesSessionsProofsAndSignedDeliveryBytes(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	request := provisioningRequest("preserved")
	d, _, err := s.ProvisionDevice(ctx, request, "original-provisioning-key")
	require.NoError(t, err)
	oldHash, err := s.provisioningHashVersion(request, 1)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE device_provisioning SET payload_hash=?,hash_version=1 WHERE connection_id=?`, oldHash, d.ConnectionID)
	require.NoError(t, err)
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "456", Token: "synthetic-token", Data: json.RawMessage(`{"cookies":[]}`)}
	require.NoError(t, s.SaveSession(ctx, d.ConnectionID, session))
	op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("retained"), "accepted-once", AdmissionLimits{Global: 20})
	require.NoError(t, err)
	_, err = s.ClaimOperations(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, s.FinishOperation(ctx, d.ConnectionID, op.ID, "unknown", nil, "SEND_UNKNOWN", "ambiguous"))
	queued, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest("still queued"), "queued-once", AdmissionLimits{Global: 20})
	require.NoError(t, err)
	scheduled := textRequest("future occurrence")
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	scheduled.ScheduledAt = next.Format(time.RFC3339)
	job, err := s.CreateScheduleIdempotent(ctx, d.ConnectionID, scheduled, next, "future-once")
	require.NoError(t, err)
	nextOccurrence := next.Add(time.Hour)
	_, err = s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, next, &nextOccurrence, AdmissionLimits{Global: 20})
	require.NoError(t, err)
	job, err = s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	occurrencesBefore, err := s.ListScheduleOccurrences(ctx, d.ConnectionID, job.ID, "", 50, 0)
	require.NoError(t, err)
	require.Len(t, occurrencesBefore, 1)
	var orderBefore int64
	require.NoError(t, s.db.QueryRow(`SELECT queue_order FROM operations WHERE id=?`, queued.ID).Scan(&orderBefore))
	m := privateMedia()
	ev := documentEvent("document", 1720000000000, &m)
	_, err = s.AppendEvent(ctx, d.ConnectionID, ev, []WebhookTarget{{URL: "https://synthetic.invalid/webhook", Secret: "synthetic-secret"}})
	require.NoError(t, err)
	// Schema-7 payloads had no provider field. Give the queued webhook distinct
	// JSON whitespace as well: an upgrade must retain the exact signed bytes.
	var oldEvent map[string]json.RawMessage
	oldBody, err := json.Marshal(ev)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(oldBody, &oldEvent))
	delete(oldEvent, "provider")
	oldBody, err = json.MarshalIndent(oldEvent, "", "  ")
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE events SET body=? WHERE connection_id=? AND id=?`, string(oldBody), d.ConnectionID, ev.ID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE deliveries SET body=? WHERE connection_id=? AND event_id=?`, string(oldBody), d.ConnectionID, ev.ID)
	require.NoError(t, err)
	before, err := s.ListDeliveries(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, before, 1)
	retired := device(t, s, "retired")
	require.NoError(t, s.DeleteDevice(ctx, retired.ConnectionID))
	_, err = s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: ptr("https://synthetic.invalid/changed")})
	require.NoError(t, err)
	restoreV7ProviderSchema(t, s)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=7`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	loaded, err := upgraded.LoadSession(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, session, loaded)
	got, err := upgraded.GetProviderMedia(ctx, d.ConnectionID, ev.Peer, ev.MessageID)
	require.NoError(t, err)
	require.Equal(t, m, got)
	checkpoint, err := upgraded.ScopedCheckpoint(ctx, d.ConnectionID, domains.DefaultCheckpointScope)
	require.NoError(t, err)
	require.Equal(t, ev.Checkpoint, checkpoint)
	current, replay, err := upgraded.ProvisionDevice(ctx, request, "original-provisioning-key")
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, d.InstanceID, current.InstanceID)
	require.Equal(t, domains.ProviderBale, current.Provider)
	require.Equal(t, "https://synthetic.invalid/changed", current.Webhook.URL)
	after, err := upgraded.ListDeliveries(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, before[0].Body, after[0].Body)
	require.Equal(t, before[0].ID, after[0].ID)
	require.Equal(t, before[0].Secret, after[0].Secret)
	retained, err := upgraded.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", retained.State)
	require.Equal(t, op.Request.RequestID, retained.Request.RequestID)
	queuedAfter, err := upgraded.GetOperation(ctx, d.ConnectionID, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", queuedAfter.State)
	require.Equal(t, queued.Request, queuedAfter.Request)
	var orderAfter int64
	require.NoError(t, upgraded.db.QueryRow(`SELECT queue_order FROM operations WHERE id=?`, queued.ID).Scan(&orderAfter))
	require.Equal(t, orderBefore, orderAfter)
	jobAfter, err := upgraded.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Equal(t, job, jobAfter)
	occurrencesAfter, err := upgraded.ListScheduleOccurrences(ctx, d.ConnectionID, job.ID, "", 50, 0)
	require.NoError(t, err)
	require.Equal(t, occurrencesBefore, occurrencesAfter)
	replayJob, err := upgraded.CreateScheduleIdempotent(ctx, d.ConnectionID, scheduled, next, "future-once")
	require.NoError(t, err)
	require.Equal(t, job.ID, replayJob.ID)
	var provider string
	require.NoError(t, upgraded.db.QueryRow(`SELECT provider FROM devices WHERE connection_id=?`, retired.ConnectionID).Scan(&provider))
	require.Equal(t, "bale", provider)
	changed := request
	changed.Provider = domains.ProviderRubika
	_, _, err = upgraded.ProvisionDevice(ctx, changed, "original-provisioning-key")
	errorCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestProviderMigrationFailureRollsBackPrivateRecordsAndSchema(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	a := device(t, s, "first")
	b := device(t, s, "second")
	for _, d := range []domains.Device{a, b} {
		require.NoError(t, s.SaveSession(ctx, d.ConnectionID, &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: d.ID, Token: "synthetic"}))
	}
	restoreV7ProviderSchema(t, s)
	_, err := s.db.Exec(`UPDATE gobale_meta SET version=7`)
	require.NoError(t, err)
	var original []byte
	require.NoError(t, s.db.QueryRow(`SELECT cipher FROM sessions WHERE connection_id=?`, a.ConnectionID).Scan(&original))
	_, err = s.db.Exec(`UPDATE sessions SET cipher=? WHERE connection_id=?`, []byte("damaged"), b.ConnectionID)
	require.NoError(t, err)
	require.Error(t, s.migrate(ctx))
	var got []byte
	require.NoError(t, s.db.QueryRow(`SELECT cipher FROM sessions WHERE connection_id=?`, a.ConnectionID).Scan(&got))
	require.Equal(t, original, got)
	var version, count int
	require.NoError(t, s.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, 7, version)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name='provider'`).Scan(&count))
	require.Zero(t, count)
	err = s.db.QueryRow(`SELECT value FROM provider_checkpoints`).Scan(new(string))
	require.Error(t, err)
	require.NotErrorIs(t, err, sql.ErrNoRows)
}
