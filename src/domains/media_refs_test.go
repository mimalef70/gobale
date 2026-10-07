package domains

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReferencedMediaIDsIncludesReviewedPayloadAndDeduplicates(t *testing.T) {
	for _, operation := range []string{"account.avatar", "group.photo", "story.add"} {
		ids, err := ReferencedMediaIDs(SendRequest{Operation: operation, MediaID: "shared", Payload: json.RawMessage(`{"media_id":"shared"}`)})
		require.NoError(t, err)
		require.Equal(t, []string{"shared"}, ids)
		ids, err = ReferencedMediaIDs(SendRequest{Operation: operation, Payload: json.RawMessage(`{"media_id":"nested"}`)})
		require.NoError(t, err)
		require.Equal(t, []string{"nested"}, ids)
	}
	ids, err := ReferencedMediaIDs(SendRequest{Operation: "send.contact", Payload: json.RawMessage(`{"quoted":{"media_id":"not-a-local-reference"}}`)})
	require.NoError(t, err)
	require.Empty(t, ids)
	_, err = ReferencedMediaIDs(SendRequest{Operation: "account.avatar", Payload: json.RawMessage(`{"media_id":123}`)})
	require.Error(t, err)
}
