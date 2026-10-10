package domains

// CheckpointTransition is an opaque provider cursor change. Expected must match
// the last durable value in Scope; an absent scope has the empty value. The
// provider owns ordering and meaning, while storage guarantees atomic acceptance.
type CheckpointTransition struct {
	Scope    string
	Expected string
	Next     string
}

const DefaultCheckpointScope = "default"

// MaxBatchEvents accommodates the reviewed Bale recovery page (4096 updates)
// and its public recovery marker. The storage byte limit is independent.
const MaxBatchEvents = 4097

// AcceptanceProof is private evidence emitted only by reviewed native adapters.
// EventID names an event in the same batch, before connection scoping. A proof
// cannot substitute content for a duplicate event's already persisted body.
type AcceptanceProof struct {
	Provider  Provider
	Kind      string
	EventID   string
	RequestID string
}

const BaleMessageEchoProof = "bale.message_echo"

// EventBatch atomically accepts a bounded page of provider changes. Empty or
// duplicate-only pages can advance an explicitly compared checkpoint without
// inventing public events. No provider cursor may advance before this commits.
type EventBatch struct {
	Events      []Event
	Checkpoints []CheckpointTransition
	Proofs      []AcceptanceProof
}
