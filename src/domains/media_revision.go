package domains

// MediaRevision is private, provider-reviewed ordering evidence for one message's
// attachment. It must come from the actual update wrapper, never a diff page's
// final checkpoint or a locally fabricated timestamp/sequence.
type MediaRevision struct {
	Scope    string
	Sequence int64
}
