package domains

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestMentionsBoundsAndOpaqueIDs(t *testing.T) {
	for _, ids := range [][]string{{"1", "1"}, {""}, {strings.Repeat("x", 257)}, {"bad\nID"}, strings.Fields(strings.Repeat("1 ", 101))} {
		require.Error(t, ValidateMentions("caption", ids))
	}
	require.Error(t, ValidateMentions("", []string{"1"}))
	require.NoError(t, ValidateMentions("caption", []string{"1", "4294967295", "u0synthetic"}))
	require.NoError(t, ValidateMentions("", nil))
}
