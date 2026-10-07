package balemeow

import (
	"encoding/hex"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEventActorPreservesUnknown(t *testing.T) {
	for _, id := range []int64{0, -1, 1 << 32} {
		sender, direction := eventActor("7", id)
		require.Empty(t, sender)
		require.Equal(t, "unknown", direction)
	}
	sender, direction := eventActor("7", 7)
	require.Equal(t, "7", sender)
	require.Equal(t, "outgoing", direction)
	sender, direction = eventActor("7", 8)
	require.Equal(t, "8", sender)
	require.Equal(t, "incoming", direction)
}

func TestLiteralEditWithoutUpdaterIsUnknown(t *testing.T) {
	// Independently encoded union field162: peer(user42), RID7, text x,
	// wrapped date1000, absent updater wrapper. No generated encoder involved.
	raw, err := hex.DecodeString("920a140a040801102a10071a057a030a0178220308e807")
	require.NoError(t, err)
	events, err := decodeEvents("7", raw)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "message.edited", events[0].Type)
	require.Empty(t, events[0].SenderID)
	require.Equal(t, "unknown", events[0].Direction)
}
