package rubikameow

import (
	"encoding/json"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestCommonMessageIdentityForRubikaEdit(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	var update object
	decoder := json.NewDecoder(strings.NewReader(`{"action":"Edit","object_guid":"u0peer","message_id":"42","timestamp":"revision","message":{"time":100,"type":"Text","text":"سلام 🙂","author_object_guid":"u0peer"}}`))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&update))
	event, err := c.projectMessage("u0peer", update)
	require.NoError(t, err)
	require.Equal(t, event.Peer.ID, event.Message.ChatID)
	require.Equal(t, event.MessageID, event.Message.ID)
	require.Equal(t, "42", event.Message.OriginalMessageID)
	require.False(t, event.Message.Partial)
	for _, raw := range []string{`{"is_edited":true}`, `{"text":""}`, `{"text":"سلام 🙂"}`} {
		var message object
		require.NoError(t, json.Unmarshal([]byte(raw), &message))
		update["message"] = message
		event, err = c.projectMessage("u0peer", update)
		require.NoError(t, err)
		require.Nil(t, event.Message)
		require.NotNil(t, event.MessagePatch)
		patch := event.MessagePatch
		require.True(t, patch.Partial)
		require.Equal(t, event.MessageID, patch.OriginalMessageID)
		require.Equal(t, event.Peer.ID, patch.ChatID)
	}
}

func TestSparseEditRejectsConflictingIdentityAndMalformedText(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	for _, message := range []object{{"text": 42}, {"message_id": "43", "text": "synthetic"}} {
		_, err := c.projectMessage("u0peer", object{"action": "Edit", "message_id": "42", "timestamp": "revision", "message": message})
		require.Error(t, err)
	}
	event, err := c.projectMessage("u0peer", object{"action": "Edit", "message_id": "42", "timestamp": "revision", "message": object{"metadata": object{"private": "not projected"}}})
	require.NoError(t, err)
	c.setRecoveryStatus(checkpoint{Version: 2, Account: "u0self"})
	before := c.Status()
	c.observeCoverage([]domains.Event{event})
	after := c.Status()
	require.True(t, after.UnsupportedUpdatesObserved)
	require.Equal(t, before.Recovery, after.Recovery)
	require.Equal(t, before.RecoveryIssue, after.RecoveryIssue)
}
