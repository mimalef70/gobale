package balemeow

// Bale uses the shared inspected Ogg Opus implementation. These package-local
// bindings also keep its independent literal-wire and fuzz regressions intact.
import "github.com/mimalef70/goomni/src/internal/mediautil"

var prepareVoiceSource = mediautil.PrepareVoiceSource
var inspectOggOpus = mediautil.InspectOggOpus
var opusPacketSamples = mediautil.OpusPacketSamples
var validOpusTags = mediautil.ValidOpusTags
var oggCRC = mediautil.OggCRC
var errInvalidVoice = mediautil.ErrInvalidVoice

const maxOpusPacketBytes = mediautil.MaxOpusPacketBytes
