package balemeow

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// Construct container fixtures, not encoded speech. The audio packets are the
// standard three-byte Opus silence packet, 20 ms each. CRC uses the slow direct
// polynomial implementation independently of the parser's lookup table.
func voicePage(flags byte, sequence uint32, granule uint64, packets ...[]byte) []byte {
	page := make([]byte, 27)
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], 0x12345678)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	var body []byte
	for _, p := range packets {
		for n := len(p); ; n -= 255 {
			if n < 255 {
				page = append(page, byte(n))
				break
			}
			page = append(page, 255)
		}
		body = append(body, p...)
	}
	page[26] = byte(len(page) - 27)
	page = append(page, body...)
	voiceFixtureCRC(page)
	return page
}

func voiceFixtureCRC(page []byte) {
	clear(page[22:26])
	var crc uint32
	for _, b := range page {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	binary.LittleEndian.PutUint32(page[22:26], crc)
}

func validVoicePages() [][]byte {
	head := append([]byte("OpusHead"), 1, 1, 0x38, 0x01, 0x80, 0xbb, 0, 0, 0, 0, 0)
	tags := append([]byte("OpusTags"), make([]byte, 8)...)
	var packets [][]byte
	for range 51 {
		packets = append(packets, []byte{0xf8, 0xff, 0xfe})
	}
	// 51*960 decoded samples; 312 pre-skip, and 648 trimmed at EOS: 1000 ms.
	return [][]byte{voicePage(2, 0, 0, head), voicePage(0, 1, 0, tags), voicePage(4, 2, 48312, packets...)}
}

func validVoiceBytes() []byte { return bytes.Join(validVoicePages(), nil) }

func TestOggOpusDurationUsesPreskipAndTrimInMilliseconds(t *testing.T) {
	body := validVoiceBytes()
	duration, err := inspectOggOpus(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	require.Equal(t, int32(1000), duration)
	pages := validVoicePages()
	binary.LittleEndian.PutUint64(pages[2][6:14], 48100)
	voiceFixtureCRC(pages[2])
	body = bytes.Join(pages, nil)
	duration, err = inspectOggOpus(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	require.Equal(t, int32(995), duration, "fractional milliseconds round down like the web client")
}

func TestOggOpusAcceptsAudioPacketContinuedAcrossPages(t *testing.T) {
	pages := validVoicePages()[:2]
	clear(pages[0][28+10 : 28+12]) // No pre-skip for this synthetic 20 ms packet.
	voiceFixtureCRC(pages[0])
	packet := append([]byte{0xf8}, make([]byte, 299)...)
	first := voicePage(0, 2, ^uint64(0), packet[:255])
	// A 255-byte fragment has one 255 segment and no terminating zero segment.
	first = append(first[:28], first[29:]...)
	first[26] = 1
	voiceFixtureCRC(first)
	last := voicePage(5, 3, 960, packet[255:])
	body := bytes.Join(append(pages, first, last), nil)
	duration, err := inspectOggOpus(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	require.Equal(t, int32(20), duration)
}

func TestOggOpusRejectsMalformedOrUnboundedInput(t *testing.T) {
	cases := map[string]func([][]byte) []byte{
		"bad capture":                   func(p [][]byte) []byte { p[0][0] = 'X'; return bytes.Join(p, nil) },
		"bad CRC":                       func(p [][]byte) []byte { p[2][len(p[2])-1] ^= 1; return bytes.Join(p, nil) },
		"truncated":                     func(p [][]byte) []byte { b := bytes.Join(p, nil); return b[:len(b)-1] },
		"trailing":                      func(p [][]byte) []byte { return append(bytes.Join(p, nil), 0) },
		"chained":                       func(p [][]byte) []byte { return append(bytes.Join(p, nil), bytes.Join(p, nil)...) },
		"missing EOS":                   func(p [][]byte) []byte { p[2][5] = 0; voiceFixtureCRC(p[2]); return bytes.Join(p, nil) },
		"missing BOS":                   func(p [][]byte) []byte { p[0][5] = 0; voiceFixtureCRC(p[0]); return bytes.Join(p, nil) },
		"serial changes":                func(p [][]byte) []byte { p[2][14]++; voiceFixtureCRC(p[2]); return bytes.Join(p, nil) },
		"sequence gap":                  func(p [][]byte) []byte { p[2][18]++; voiceFixtureCRC(p[2]); return bytes.Join(p, nil) },
		"continuation without fragment": func(p [][]byte) []byte { p[2][5] |= 1; voiceFixtureCRC(p[2]); return bytes.Join(p, nil) },
		"invented granule": func(p [][]byte) []byte {
			binary.LittleEndian.PutUint64(p[2][6:14], 99999999)
			voiceFixtureCRC(p[2])
			return bytes.Join(p, nil)
		},
		"no audible duration": func(p [][]byte) []byte {
			binary.LittleEndian.PutUint64(p[2][6:14], 300)
			voiceFixtureCRC(p[2])
			return bytes.Join(p, nil)
		},
		"too much end trimming": func(p [][]byte) []byte {
			binary.LittleEndian.PutUint64(p[2][6:14], 47000)
			voiceFixtureCRC(p[2])
			return bytes.Join(p, nil)
		},
		"multichannel":   func(p [][]byte) []byte { p[0][28+9] = 6; voiceFixtureCRC(p[0]); return bytes.Join(p, nil) },
		"mapping family": func(p [][]byte) []byte { p[0][28+18] = 1; voiceFixtureCRC(p[0]); return bytes.Join(p, nil) },
		"not Opus":       func(p [][]byte) []byte { copy(p[0][28:], "NotAHead"); voiceFixtureCRC(p[0]); return bytes.Join(p, nil) },
		"bad Tags":       func(p [][]byte) []byte { p[1][28+12] = 255; voiceFixtureCRC(p[1]); return bytes.Join(p, nil) },
		"oversize audio packet": func(p [][]byte) []byte {
			p[2] = voicePage(4, 2, 960, append([]byte{0xf8}, make([]byte, 1276)...))
			return bytes.Join(p, nil)
		},
		"invalid packet framing": func(p [][]byte) []byte { p[2] = voicePage(4, 2, 960, []byte{0xfb, 0}); return bytes.Join(p, nil) },
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			body := modify(validVoicePages())
			_, err := inspectOggOpus(bytes.NewReader(body), int64(len(body)))
			require.Error(t, err)
		})
	}
	for _, delta := range []int64{-1, 1, -int64(len(validVoiceBytes()))} {
		body := validVoiceBytes()
		_, err := inspectOggOpus(bytes.NewReader(body), int64(len(body))+delta)
		require.Error(t, err, "metadata must exactly match source bytes")
	}
}

func TestOpusPacketLengthsAndDurations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		packet  []byte
		samples uint64
	}{
		{"silence", []byte{0xf8, 0xff, 0xfe}, 960},
		{"2.5ms", []byte{0x80, 1}, 120},
		{"CBR two frames", []byte{0xf9, 1, 2}, 1920},
		{"VBR two frames", []byte{0xfa, 1, 2, 3}, 1920},
		{"arbitrary CBR", []byte{0xfb, 3, 1, 2, 3}, 2880},
		{"arbitrary VBR", []byte{0xfb, 0x83, 1, 1, 2, 3, 4}, 2880},
		{"padded CBR", []byte{0xfb, 0x42, 2, 1, 2, 0, 0}, 1920},
		{"empty packet", nil, 0},
		{"CBR odd bytes", []byte{0xf9, 1}, 0},
		{"VBR exceeds bytes", []byte{0xfa, 4, 2}, 0},
		{"missing VBR length", []byte{0xfa, 252}, 0},
		{"missing count", []byte{0xfb}, 0},
		{"zero frames", []byte{0xfb, 0}, 0},
		{"over 120 ms", []byte{0xfb, 7}, 0},
		{"missing padding", []byte{0xfb, 0x41}, 0},
		{"too much padding", []byte{0xfb, 0x41, 8}, 0},
		{"missing VBR declarations", []byte{0xfb, 0x83, 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, err := opusPacketSamples(tc.packet)
			if tc.samples == 0 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.samples, samples)
			}
		})
	}
	malformedTags := append([]byte("OpusTags"), 0, 0, 0, 0, 2, 0, 0, 0, 4, 0, 0, 0, 1, 2, 3, 4)
	require.False(t, validOpusTags(malformedTags), "later comment length cannot index past the buffer")
}

func TestPrepareVoiceSourceRewindsAndCleansPrivateTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	body := validVoiceBytes()
	for _, seekable := range []bool{true, false} {
		var reader io.Reader = bytes.NewReader(body)
		if !seekable {
			reader = io.NopCloser(reader)
		}
		prepared, duration, cleanup, err := prepareVoiceSource(context.Background(), reader, int64(len(body)))
		require.NoError(t, err)
		require.Equal(t, int32(1000), duration)
		got, err := io.ReadAll(prepared)
		require.NoError(t, err)
		require.Equal(t, body, got)
		files, err := filepath.Glob(filepath.Join(dir, "goomni-voice-*"))
		require.NoError(t, err)
		if seekable {
			require.Empty(t, files)
		} else {
			require.Len(t, files, 1)
			stat, err := os.Stat(files[0])
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
		}
		cleanup()
		files, err = filepath.Glob(filepath.Join(dir, "goomni-voice-*"))
		require.NoError(t, err)
		require.Empty(t, files)
	}
	_, _, cleanup, err := prepareVoiceSource(context.Background(), io.NopCloser(bytes.NewReader([]byte("invalid"))), 7)
	require.Equal(t, "INVALID_MEDIA", codeOf(err))
	cleanup()
	files, err := filepath.Glob(filepath.Join(dir, "goomni-voice-*"))
	require.NoError(t, err)
	require.Empty(t, files)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, cleanup, err = prepareVoiceSource(ctx, io.NopCloser(bytes.NewReader(body)), int64(len(body)))
	require.ErrorIs(t, err, context.Canceled)
	cleanup()
}

// Read raw wire field numbers independently of the generated Voice schema.
func voiceBytesField(t *testing.T, p []byte, field protowire.Number) []byte {
	t.Helper()
	for len(p) > 0 {
		number, typ, n := protowire.ConsumeTag(p)
		require.Greater(t, n, 0)
		p = p[n:]
		if number == field {
			require.Equal(t, protowire.BytesType, typ)
			b, n := protowire.ConsumeBytes(p)
			require.Greater(t, n, 0)
			return b
		}
		n = protowire.ConsumeFieldValue(number, typ, p)
		require.Greater(t, n, 0)
		p = p[n:]
	}
	t.Fatalf("missing protobuf field %d", field)
	return nil
}

func TestNativeVoiceUsesVoiceWireTypeAndRealDuration(t *testing.T) {
	body := validVoiceBytes()
	var uploads, messages atomic.Int32
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, body, got)
		w.WriteHeader(200)
	}))
	defer tls.Close()
	var f *fakeWS
	f = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if m.Request == nil {
			return
		}
		switch m.Request.Method {
		case "GetNasimFileUploadUrl":
			var req wire.FileUploadRequest
			require.NoError(t, decode(m.Request.Payload, &req))
			require.Equal(t, int32(3), req.SendType.Type)
			require.Equal(t, []byte{0x08, 0x03}, voiceBytesField(t, m.Request.Payload, 7))
			require.Equal(t, "audio/ogg", req.MimeType)
			respond(t, f, ws, m.Request, &wire.FileUploadResponse{FileId: 11, Url: tls.URL})
		case "SendMessage":
			messages.Add(1)
			var req wire.SendMessageRequest
			require.NoError(t, decode(m.Request.Payload, &req))
			require.Equal(t, int32(1000), req.Message.Document.Ext.Voice.Duration)
			require.Nil(t, req.Message.Document.Ext.Audio)
			require.Equal(t, "caption", req.Message.Document.Caption.Text)
			require.Equal(t, int64(222), req.QuotedMessage.Rid)
			// SendMessage.message=3, Message.document=4, Document.ext=7,
			// DocumentExt.voice=3, Voice.duration=1. Literal bytes encode1000ms.
			message := voiceBytesField(t, m.Request.Payload, 3)
			doc := voiceBytesField(t, message, 4)
			ext := voiceBytesField(t, doc, 7)
			voice := voiceBytesField(t, ext, 3)
			require.Equal(t, []byte{0x08, 0xe8, 0x07}, voice)
			respond(t, f, ws, m.Request, &wire.SendMessageResponse{Date: 1720000000000})
		}
	})
	c := mediaClient(t, f, tls, body, MediaInfo{Name: "voice.ogg", ContentType: "audio/ogg; codecs=opus", Size: int64(len(body)), Duration: 999999})
	_, err := c.Send(context.Background(), mediaRequest("voice"))
	require.NoError(t, err)
	require.Equal(t, int32(1), uploads.Load())
	require.Equal(t, int32(1), messages.Load())
}

func TestInvalidVoiceRejectedBeforeProviderUpload(t *testing.T) {
	for _, tc := range []struct {
		mime string
		body []byte
		code string
	}{
		{"audio/mpeg", validVoiceBytes(), "VOICE_FORMAT_NOT_SUPPORTED"},
		{"audio/ogg; codecs=vorbis", validVoiceBytes(), "VOICE_FORMAT_NOT_SUPPORTED"},
		{"audio/ogg", []byte("ID3 not an Ogg file"), "INVALID_MEDIA"},
	} {
		var calls atomic.Int32
		f := newFakeWS(t, func(*websocket.Conn, *wire.ClientMessage) { calls.Add(1) })
		tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid voice reached upload") }))
		c := mediaClient(t, f, tls, tc.body, MediaInfo{Name: "voice.ogg", ContentType: tc.mime, Size: int64(len(tc.body))})
		_, err := c.Send(context.Background(), mediaRequest("voice"))
		require.Equal(t, tc.code, codeOf(err))
		require.Zero(t, calls.Load())
		tls.Close()
	}
}

func FuzzOggOpus(f *testing.F) {
	f.Add(validVoiceBytes())
	f.Add([]byte("OpusHead"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = inspectOggOpus(bytes.NewReader(data), int64(len(data)))
	})
}

func FuzzOpusPacket(f *testing.F) {
	f.Add([]byte{0xf8, 0xff, 0xfe})
	f.Add([]byte{0xfb, 0x83, 1, 1, 2, 3, 4})
	f.Add(append([]byte("OpusTags"), make([]byte, 8)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxOpusPacketBytes {
			t.Skip()
		}
		_, _ = opusPacketSamples(data)
		_ = validOpusTags(data)
	})
}
