package storage

const operationQueueOrderIndex = `CREATE UNIQUE INDEX operations_queue_order ON operations(queue_order)`
const operationPendingIndex = `CREATE INDEX operations_pending_order ON operations(connection_id,queue_order) WHERE state='queued'`
const operationInflightIndex = `CREATE INDEX operations_inflight ON operations(connection_id) WHERE state='sending'`
const operationScopeIndex = `CREATE UNIQUE INDEX operations_scope_id ON operations(connection_id,id)`
const scheduleScopeIndex = `CREATE UNIQUE INDEX schedules_scope_id ON schedules(connection_id,id)`
const occurrenceSchema = `CREATE TABLE schedule_occurrences(connection_id TEXT NOT NULL,schedule_id TEXT NOT NULL,occurrence_number INTEGER NOT NULL CHECK(occurrence_number>0),scheduled_for INTEGER NOT NULL,operation_id TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(connection_id,schedule_id,scheduled_for),UNIQUE(connection_id,schedule_id,occurrence_number),UNIQUE(connection_id,operation_id),FOREIGN KEY(connection_id,schedule_id) REFERENCES schedules(connection_id,id),FOREIGN KEY(connection_id,operation_id) REFERENCES operations(connection_id,id))`
const mediaPathIndex = `CREATE INDEX media_path ON media(path)`
const eventOrderIndex = `CREATE INDEX events_connection_time ON events(connection_id,event_time DESC,id)`

var apiFeatureMigration = []string{
	`ALTER TABLE operations ADD COLUMN queue_order INTEGER NOT NULL DEFAULT 0`,
	`WITH positions AS MATERIALIZED (SELECT rowid AS source_rowid,ROW_NUMBER() OVER(ORDER BY created_at,rowid) AS position FROM operations) UPDATE operations SET queue_order=(SELECT position FROM positions WHERE source_rowid=operations.rowid)`,
	operationQueueOrderIndex, operationPendingIndex, operationInflightIndex, operationScopeIndex, scheduleScopeIndex, occurrenceSchema, eventOrderIndex, mediaPathIndex,
	`ALTER TABLE devices ADD COLUMN webhook_filter TEXT NOT NULL DEFAULT '{}'`,
}
