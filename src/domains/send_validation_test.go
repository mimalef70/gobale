package domains

import (
	"errors"
	"strings"
	"testing"
)

func TestSendMessageAndMediaCaptionShareByteLimit(t *testing.T) {
	for _, kind := range []string{"", "text", "image", "file", "audio", "video", "voice"} {
		t.Run("kind="+kind, func(t *testing.T) {
			r := SendRequest{Peer: Peer{Type: "user", ID: "42"}, Kind: kind, MediaID: "owned-media", Text: strings.Repeat("a", 65536)}
			if err := r.Validate(); err != nil {
				t.Fatal("exact limit rejected", err)
			}
			r.Text += "a"
			var de *Error
			if err := r.Validate(); !errors.As(err, &de) || de.Code != "INVALID_REQUEST" || de.HTTP != 400 {
				t.Fatal("oversized message/caption accepted", err)
			}
			// Persian UTF-8 text must be bounded by bytes, not rune count.
			r.Text = strings.Repeat("ب", 32769)
			if err := r.Validate(); err == nil {
				t.Fatal("oversized multi-byte caption accepted")
			}
			r.Text = ""
			err := r.Validate()
			if (kind == "" || kind == "text") != (err != nil) {
				t.Fatal("empty text requirement or optional caption changed", err)
			}
		})
	}
}
