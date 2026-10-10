package balemeow

import (
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strconv"
)

// Called only after SendRequest.Validate, including for media captions.
func mentionedText(text string, mentions []string) *wire.TextMessage {
	message := &wire.TextMessage{Text: text}
	for _, id := range mentions {
		n, _ := strconv.ParseUint(id, 10, 32)
		message.Mentions = append(message.Mentions, uint32(n))
	}
	return message
}
