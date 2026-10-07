package domains

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestMentionsBoundsAndCanonicalIDs(t *testing.T) {
	for _, ids := range [][]string{{"0"}, {"01"}, {"+1"}, {"-1"}, {"1", "1"}, {"4294967296"}, {""}, strings.Fields(strings.Repeat("1 ", 101))} {
		require.Error(t, ValidateMentions("caption", ids))
	}
	require.Error(t, ValidateMentions("", []string{"1"}))
	require.NoError(t, ValidateMentions("caption", []string{"1", "4294967295"}))
	require.NoError(t, ValidateMentions("", nil))
}
