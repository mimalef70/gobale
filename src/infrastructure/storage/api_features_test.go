package storage

import (
	"context"
	"encoding/json"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// Downgrade only synthetic fixtures. Runtime migrations reject partial schemas.
func restoreV5APISchema(t *testing.T, s *Store) {
	t.Helper()
	restoreV6ProvisioningSchema(t, s)
	for _, ddl := range []string{`DROP TABLE schedule_occurrences`, `DROP INDEX operations_scope_id`, `DROP INDEX schedules_scope_id`, `DROP INDEX operations_queue_order`, `DROP INDEX operations_pending_order`, `DROP INDEX operations_inflight`, `ALTER TABLE operations DROP COLUMN queue_order`, `DROP INDEX events_connection_time`, `DROP INDEX media_path`, `ALTER TABLE devices DROP COLUMN webhook_filter`} {
		_, err := s.db.Exec(ddl)
		require.NoError(t, err)
	}
}
func TestOperationOrderMigrationAndVacuum(t *testing.T) {
	ctx := context.Background()
	s, path := testStore(t)
	d := device(t, s, "order")
	var ids []string
	for _, name := range []string{"later", "first", "second"} {
		op, _, err := s.Enqueue(ctx, d.ConnectionID, textRequest(name), name, AdmissionLimits{Global: 10})
		require.NoError(t, err)
		ids = append(ids, op.ID)
	}
	restoreV5APISchema(t, s)
	_, err := s.db.Exec(`UPDATE operations SET created_at=100`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE operations SET created_at=200 WHERE id=?`, ids[0])
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE gobale_meta SET version=5`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	upgraded, err := Open(path, testKey)
	require.NoError(t, err)
	defer upgraded.Close()
	var version int
	require.NoError(t, upgraded.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, schemaVersion, version)
	_, err = upgraded.db.Exec(`VACUUM`)
	require.NoError(t, err)
	for _, id := range []string{ids[1], ids[2], ids[0]} {
		ops, err := upgraded.ClaimOperations(ctx, 1)
		require.NoError(t, err)
		require.Len(t, ops, 1)
		require.Equal(t, id, ops[0].ID)
		require.NoError(t, upgraded.FinishOperation(ctx, d.ConnectionID, id, "failed", nil, "TEST", "synthetic"))
	}
}
func TestScheduleOccurrenceAtomicMappingNoPublicKeyCollision(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "schedule")
	other := device(t, s, "other")
	req := domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "text", Text: "synthetic"}
	due := time.UnixMilli(123456).UTC()
	job, err := s.CreateSchedule(ctx, d.ConnectionID, req, due)
	require.NoError(t, err)
	fake, _, err := s.Enqueue(ctx, d.ConnectionID, req, "schedule:"+job.ID+":123456", AdmissionLimits{Global: 10})
	require.NoError(t, err)
	_, err = s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, nil, AdmissionLimits{Global: 1})
	errorCode(t, err, "QUEUE_FULL")
	unchanged, err := s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.Zero(t, unchanged.Count)
	occ, err := s.ListScheduleOccurrences(ctx, d.ConnectionID, job.ID, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, occ)
	_, err = s.db.Exec(`CREATE TRIGGER occurrence_failure BEFORE INSERT ON schedule_occurrences BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`)
	require.NoError(t, err)
	_, err = s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, nil, AdmissionLimits{Global: 10})
	require.Error(t, err)
	ops, err := s.ListOperations(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	_, err = s.db.Exec(`DROP TRIGGER occurrence_failure`)
	require.NoError(t, err)
	op, err := s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, nil, AdmissionLimits{Global: 10})
	require.NoError(t, err)
	require.NotEqual(t, fake.ID, op.ID)
	require.NotEqual(t, fake.Request.RequestID, op.Request.RequestID)
	_, err = s.MaterializeSchedule(ctx, d.ConnectionID, job.ID, due, nil, AdmissionLimits{Global: 10})
	errorCode(t, err, "SCHEDULE_CONFLICT")
	got, err := s.GetOperation(ctx, d.ConnectionID, op.ID)
	require.NoError(t, err)
	require.Equal(t, job.ID, got.ScheduleID)
	require.Equal(t, due, *got.ScheduledFor)
	ops, err = s.ListOperationsFiltered(ctx, d.ConnectionID, domains.OperationFilter{ScheduleID: job.ID, State: "queued", Kind: "text", Peer: "user:42"})
	require.NoError(t, err)
	require.Len(t, ops, 1)
	_, err = s.ListOperationsFiltered(ctx, other.ConnectionID, domains.OperationFilter{ScheduleID: job.ID})
	errorCode(t, err, "NOT_FOUND")
	_, err = s.db.Exec(`UPDATE operations SET state='unknown' WHERE id=?`, op.ID)
	require.NoError(t, err)
	occ, err = s.ListScheduleOccurrences(ctx, d.ConnectionID, job.ID, "unknown", 10, 0)
	require.NoError(t, err)
	require.Len(t, occ, 1)
	require.Equal(t, 1, occ[0].Number)
	require.Equal(t, "unknown", occ[0].Operation.State)
	done, err := s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.True(t, done.OccurrenceHistoryComplete)
	require.Equal(t, "completed", done.State)
	_, err = s.db.Exec(`DELETE FROM schedule_occurrences`)
	require.NoError(t, err)
	done, err = s.GetSchedule(ctx, d.ConnectionID, job.ID)
	require.NoError(t, err)
	require.False(t, done.OccurrenceHistoryComplete)
}
func TestLocalEventSearchLiteralCombinedScopedAudit(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "events")
	other := device(t, s, "other")
	bodies := []string{`{"kind":"text","message":"سلام %_🙂","quoted_message":{"content":{"message":"secret quoted"}}}`, `{"kind":"document","caption":"سلام %_🙂"}`, `{"kind":"text","message":"revised"}`, `{}`}
	for i, body := range bodies {
		ev := event(string(rune('a'+i)), "")
		ev.MessageID = "123"
		ev.Peer = domains.Peer{Type: "user", ID: "42"}
		ev.SenderID = "7"
		ev.Direction = "incoming"
		ev.Time = time.UnixMilli(int64(100 + i))
		ev.Payload = json.RawMessage(body)
		if i == 2 {
			ev.Type = "message.edited"
		}
		if i == 3 {
			ev.Type = "message.deleted"
			ev.Direction = "unknown"
			ev.SenderID = ""
		}
		_, err := s.AppendEvent(ctx, d.ConnectionID, ev, nil)
		require.NoError(t, err)
	}
	start, end := time.UnixMilli(100), time.UnixMilli(102)
	f := domains.EventFilter{Peer: "user:42", Search: "سلام %_🙂", Direction: "incoming", SenderID: "7", StartTime: &start, EndTime: &end, Limit: 1}
	got, err := s.ListEventsFiltered(ctx, d.ConnectionID, f)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "document", func() string {
		var p map[string]any
		require.NoError(t, json.Unmarshal(got[0].Payload, &p))
		return p["kind"].(string)
	}())
	f.Offset = 1
	got, err = s.ListEventsFiltered(ctx, d.ConnectionID, f)
	require.NoError(t, err)
	require.Len(t, got, 1)
	f.MediaOnly = true
	got, err = s.ListEventsFiltered(ctx, d.ConnectionID, f)
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = s.ListEventsFiltered(ctx, other.ConnectionID, domains.EventFilter{Search: "سلام"})
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{Search: "secret quoted"})
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{})
	require.NoError(t, err)
	require.Len(t, got, 4, "edit/delete do not erase event history")
}
func TestWebhookFilterAtomicCurrentRoutingAndReplay(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "filter")
	url := "https://device.test/hook"
	secret := "synthetic"
	_, err := s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{URL: &url, Secret: &secret, Filter: &domains.WebhookFilter{Directions: []string{"outgoing"}}})
	require.NoError(t, err)
	plan := []WebhookTarget{{Device: true, CurrentDevice: true}, {URL: "https://global.test/hook"}}
	ev := event("denied", "cp-denied")
	ev.SenderID = "7"
	ev.Direction = "incoming"
	_, err = s.AppendEvent(ctx, d.ConnectionID, ev, plan)
	require.NoError(t, err)
	cp, err := s.Checkpoint(ctx, d.ConnectionID)
	require.NoError(t, err)
	require.Equal(t, "cp-denied", cp)
	jobs, err := s.ListDeliveries(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, jobs, "rejecting device filter must not fall back to global")
	_, err = s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{Filter: &domains.WebhookFilter{SenderIDs: []string{"7"}}})
	require.NoError(t, err)
	ev.ID = "allowed"
	_, err = s.AppendEvent(ctx, d.ConnectionID, ev, plan)
	require.NoError(t, err)
	jobs, err = s.ListDeliveries(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	_, err = s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{Filter: &domains.WebhookFilter{ExcludeSenderIDs: []string{"7"}}})
	require.NoError(t, err)
	_, err = s.ReplayDelivery(ctx, d.ConnectionID, jobs[0].ID, plan)
	errorCode(t, err, "NO_WEBHOOK_TARGETS")
	claimed, err := s.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Len(t, claimed, 1, "changing filters must not cancel durable deliveries")
	plan[0].MergeGlobal = true
	ev.ID = "merged"
	_, err = s.AppendEvent(ctx, d.ConnectionID, ev, plan)
	require.NoError(t, err)
	jobs, err = s.ListDeliveries(ctx, d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	require.ElementsMatch(t, []string{"https://global.test/hook", "https://device.test/hook"}, []string{jobs[0].URL, jobs[1].URL})
}

func TestAPIMigrationFailureRollsBackVersionAndColumns(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	restoreV5APISchema(t, s)
	_, err := s.db.Exec(`UPDATE gobale_meta SET version=5`)
	require.NoError(t, err)
	// A conflicting DDL object forces failure after ALTER and backfill started.
	_, err = s.db.Exec(`CREATE INDEX operations_queue_order ON operations(created_at)`)
	require.NoError(t, err)
	require.Error(t, s.migrate(ctx))
	var version, columns int
	require.NoError(t, s.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, 5, version)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('operations') WHERE name='queue_order'`).Scan(&columns))
	require.Zero(t, columns)
	_, err = s.db.Exec(`DROP INDEX operations_queue_order`)
	require.NoError(t, err)
	require.NoError(t, s.migrate(ctx))
	require.NoError(t, s.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&version))
	require.Equal(t, schemaVersion, version)
}
func TestWorkFiltersTimeBoundariesAndOccurrenceScope(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "filter-work")
	other := device(t, s, "other-work")
	req := domains.SendRequest{Kind: "text", Peer: domains.Peer{Type: "user", ID: "42"}, Text: "synthetic"}
	op, _, err := s.Enqueue(ctx, d.ConnectionID, req, "one", AdmissionLimits{Global: 10})
	require.NoError(t, err)
	start := op.CreatedAt
	end := start.Add(time.Millisecond)
	got, err := s.ListOperationsFiltered(ctx, d.ConnectionID, domains.OperationFilter{State: "queued", Kind: "text", Peer: "user:42", CreatedAfter: &start, CreatedBefore: &end})
	require.NoError(t, err)
	require.Len(t, got, 1)
	fractional := start.Add(time.Microsecond)
	got, err = s.ListOperationsFiltered(ctx, d.ConnectionID, domains.OperationFilter{CreatedAfter: &fractional})
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = s.ListOperationsFiltered(ctx, d.ConnectionID, domains.OperationFilter{CreatedBefore: &fractional})
	require.NoError(t, err)
	require.Len(t, got, 1)
	job, err := s.CreateSchedule(ctx, d.ConnectionID, req, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO schedule_occurrences(connection_id,schedule_id,occurrence_number,scheduled_for,operation_id,created_at) VALUES(?,?,1,1,?,1)`, other.ConnectionID, job.ID, op.ID)
	require.Error(t, err, "composite foreign keys must prevent cross-connection attribution")
	jobs, err := s.ListSchedulesFiltered(ctx, d.ConnectionID, domains.ScheduleFilter{State: "active", Kind: "text", Peer: "user:42"})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
}
func TestHistoricalUnknownSenderSearchMatchesWebhookPredicate(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	d := device(t, s, "unknown-actor")
	for i, sender := range []string{"", "0", "01", "-1", "4294967296"} {
		ev := event(string(rune('a'+i)), "")
		ev.SenderID = sender
		ev.Direction = "incoming"
		_, err := s.AppendEvent(ctx, d.ConnectionID, ev, nil)
		require.NoError(t, err)
	}
	got, err := s.ListEventsFiltered(ctx, d.ConnectionID, domains.EventFilter{Direction: "unknown"})
	require.NoError(t, err)
	require.Len(t, got, 5)
	for _, ev := range got {
		require.True(t, (domains.WebhookFilter{Directions: []string{"unknown"}}).Matches(ev))
		require.Equal(t, "incoming", ev.Direction, "do not rewrite persisted audit bodies")
	}
}
