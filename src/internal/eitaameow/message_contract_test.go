package eitaameow

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestCommonMessageIdentityForEitaaEdit(t *testing.T) {
	c, err := New(Config{})
	require.NoError(t, err)
	// Independent received-object fixture; no generated TL encoder.
	for _, kind := range []string{"message", "message.edited"} {
		var raw object
		decoder := json.NewDecoder(strings.NewReader(`{"_":"message","id":42,"peer_id":{"_":"peerUser","user_id":7},"from_id":{"_":"peerUser","user_id":7},"date":100,"edit_date":101,"message":"سلام 🙂","out":false}`))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&raw))
		raw["peer_id"] = object(raw["peer_id"].(map[string]any))
		raw["from_id"] = object(raw["from_id"].(map[string]any))
		event, err := c.projectMessage(raw, kind)
		require.NoError(t, err)
		require.Equal(t, event.Peer.ID, event.Message.ChatID)
		require.Equal(t, event.MessageID, event.Message.ID)
		require.False(t, event.Message.Partial)
		if kind == "message.edited" {
			require.Equal(t, "42", event.Message.OriginalMessageID)
		} else {
			require.Empty(t, event.Message.OriginalMessageID)
		}
	}
}

func TestGenericDocumentUsesCommonFileProjectionWithoutChangingPrivateReference(t *testing.T) {
	c, err := New(Config{})
	require.NoError(t, err)
	ref, public := c.projectMedia(object{"_": "messageMediaDocument", "document": object{"_": "document", "id": 42, "access_hash": 77, "file_reference": []byte{1, 2}, "size": 12, "mime_type": "text/plain", "attributes": []object{{"_": "documentAttributeFilename", "file_name": "synthetic.txt"}}}})
	require.NotNil(t, ref)
	require.NotNil(t, public)
	require.Equal(t, "file", public.Type)
	var private mediaReference
	require.NoError(t, json.Unmarshal(ref.Data, &private))
	require.Equal(t, "document", private.Kind)
}
