package eitaameow

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/mediautil"
)

func syntheticPNG(t *testing.T) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 2, 3))
	m.Set(0, 0, color.RGBA{R: 120, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, m); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func syntheticOpus() []byte {
	page := func(seq uint32, flags byte, granule uint64, packet []byte) []byte {
		b := make([]byte, 28+len(packet))
		copy(b, "OggS")
		b[5] = flags
		binary.LittleEndian.PutUint64(b[6:], granule)
		binary.LittleEndian.PutUint32(b[14:], 1)
		binary.LittleEndian.PutUint32(b[18:], seq)
		b[26] = 1
		b[27] = byte(len(packet))
		copy(b[28:], packet)
		binary.LittleEndian.PutUint32(b[22:], mediautil.OggCRC(0, b))
		return b
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 1
	binary.LittleEndian.PutUint32(head[12:], 48000)
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	out := page(0, 2, 0, head)
	out = append(out, page(1, 0, 0, tags)...)
	return append(out, page(2, 4, 960, []byte{0xf8, 0xff, 0xfe})...)
}
func mediaClientSource(c *Client, body []byte) {
	c.session.Token = "synthetic-token"
	c.session.UserID = "42"
	c.cfg.MediaSource = func(context.Context, string) (io.ReadCloser, domains.NativeMediaInfo, error) {
		return io.NopCloser(bytes.NewReader(body)), domains.NativeMediaInfo{Name: "synthetic.bin", ContentType: "application/octet-stream", Size: int64(len(body))}, nil
	}
}
func mediaRequest(kind string) domains.SendRequest {
	return domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: kind, MediaID: "owned-local", RequestID: "7812345", Text: "synthetic"}
}

func TestInspectedVoiceAudioAndImage(t *testing.T) {
	for _, kind := range []string{"voice", "audio", "image"} {
		t.Run(kind, func(t *testing.T) {
			body := syntheticOpus()
			if kind == "image" {
				body = syntheticPNG(t)
			}
			var calls atomic.Int32
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls.Add(1)
				switch method {
				case "upload.saveFilePart":
					if p.num("totalFileSize") != int64(len(body)) || p.num("file_id") != 7812345 {
						t.Error("upload metadata changed")
					}
					return object{"_": "boolTrue"}, 200
				case "messages.sendMedia":
					media := asObject(p["media"])
					if kind == "image" {
						if media.str("_") != "inputMediaUploadedPhoto" {
							t.Error("not a photo")
						}
					} else {
						if media.str("mime_type") != "audio/ogg" {
							t.Error("uninspected MIME")
						}
						attrs := asObjects(media["attributes"])
						if len(attrs) != 2 || attrs[0].str("_") != "documentAttributeAudio" || attrs[0].num("duration") != 1 || (attrs[0]["voice"] == true) != (kind == "voice") {
							t.Errorf("wrong inspected audio attributes: %#v", attrs)
						}
					}
					return object{"_": "updateShortSentMessage", "id": 8, "pts": 1, "pts_count": 1, "date": 100}, 200
				}
				t.Errorf("unexpected %s", method)
				return nil, 500
			})
			mediaClientSource(c, body)
			result, e := c.Send(context.Background(), mediaRequest(kind))
			if e != nil || result.MessageID != "8" || calls.Load() != 2 {
				t.Fatalf("send: %#v %v calls=%d", result, e, calls.Load())
			}
		})
	}
}
func TestMalformedMediaRejectedBeforeUpload(t *testing.T) {
	for _, kind := range []string{"voice", "audio", "image", "video"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := nativeFixture(t, func(string, object) (object, int) { t.Error("invalid media reached provider"); return nil, 500 })
			mediaClientSource(c, []byte("arbitrary-mime-does-not-invent-duration"))
			_, e := c.Send(context.Background(), mediaRequest(kind))
			var de *domains.Error
			if !errors.As(e, &de) || de.Code != "INVALID_MEDIA" || de.Ambiguous {
				t.Fatalf("wrong validation outcome: %v", e)
			}
		})
	}
}
func TestAudioAttributeLiteral(t *testing.T) {
	c, e := bundledCodec()
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e = c.encodeDefinition(&out, c.names["documentAttributeAudio"], object{"voice": true, "duration": 1}, 0); e != nil {
		t.Fatal(e)
	}
	want := []byte{0xc6, 0xf9, 0x52, 0x98, 0, 4, 0, 0, 1, 0, 0, 0}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("voice literal mismatch: %x", out.Bytes())
	}
}
func TestUploadProfileAndFirstPartMetadata(t *testing.T) {
	var calls atomic.Int32
	c, server := nativeFixture(t, func(method string, p object) (object, int) {
		n := calls.Add(1)
		if method == "upload.saveFilePart" {
			if n == 1 {
				if _, ok := p["peer"]; !ok {
					t.Error("first part missing peer")
				}
			} else {
				if _, ok := p["peer"]; ok {
					t.Error("later part repeated metadata")
				}
				if _, ok := p["totalFileSize"]; ok {
					t.Error("later part repeated size")
				}
			}
			return object{"_": "boolTrue"}, 200
		}
		if method == "messages.sendMedia" {
			return object{"_": "updateShortSentMessage", "id": 8, "pts": 1, "pts_count": 1, "date": 100}, 200
		}
		t.Errorf("unexpected %s", method)
		return nil, 500
	})
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		v, e := c.codec.decode(raw, "EitaaObject")
		if e != nil || asObject(v).num("flags") != 128 {
			t.Errorf("wrong media envelope: %#v %v", v, e)
		}
		req, e := http.NewRequestWithContext(r.Context(), http.MethodPost, server.URL, bytes.NewReader(raw))
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer upload.Close()
	c.cfg.Endpoint = "http://127.0.0.1:1/unavailable"
	c.cfg.UploadEndpoint = upload.URL
	mediaClientSource(c, bytes.Repeat([]byte{1}, (64<<10)+7))
	_, e := c.Send(context.Background(), mediaRequest("file"))
	if e != nil || calls.Load() != 3 {
		t.Fatalf("upload profile not used for whole transaction: %v calls=%d", e, calls.Load())
	}
}
func box(kind string, p ...[]byte) []byte {
	size := 8
	for _, a := range p {
		size += len(a)
	}
	b := make([]byte, 8, size)
	binary.BigEndian.PutUint32(b, uint32(size))
	copy(b[4:], kind)
	for _, a := range p {
		b = append(b, a...)
	}
	return b
}
func syntheticMP4() []byte {
	mdhd := make([]byte, 24)
	binary.BigEndian.PutUint32(mdhd[12:], 1000)
	binary.BigEndian.PutUint32(mdhd[16:], 2000)
	hdlr := make([]byte, 12)
	copy(hdlr[8:], "vide")
	avc := make([]byte, 78)
	binary.BigEndian.PutUint16(avc[24:], 320)
	binary.BigEndian.PutUint16(avc[26:], 240)
	sd := make([]byte, 8)
	binary.BigEndian.PutUint32(sd[4:], 1)
	sd = append(sd, box("avc1", avc, box("avcC", []byte{1, 66, 0, 30, 255, 225, 0}))...)
	ts := make([]byte, 16)
	binary.BigEndian.PutUint32(ts[4:], 1)
	binary.BigEndian.PutUint32(ts[8:], 50)
	binary.BigEndian.PutUint32(ts[12:], 40)
	moov := box("moov", box("trak", box("mdia", box("mdhd", mdhd), box("hdlr", hdlr), box("minf", box("stbl", box("stsd", sd), box("stts", ts))))))
	return append(append(box("ftyp", []byte("isom\x00\x00\x00\x00isomavc1")), box("mdat", []byte{1, 2, 3})...), moov...)
}
func TestVideoInspectionUsesContainerTimingAndBounds(t *testing.T) {
	b := syntheticMP4()
	m, e := mediautil.InspectMP4(bytes.NewReader(b), int64(len(b)))
	if e != nil || m.DurationSeconds != 2 || m.DurationMilliseconds != 2000 || m.Width != 320 || m.Height != 240 {
		t.Fatalf("metadata %#v %v", m, e)
	}
	variants := [][]byte{b[:len(b)-1], append(append([]byte{}, b...), box("moof", nil)...)}
	timing := bytes.Clone(b)
	i := bytes.Index(timing, []byte("stts"))
	binary.BigEndian.PutUint32(timing[i+16:], 41)
	variants = append(variants, timing)
	huge := bytes.Clone(b)
	i = bytes.Index(huge, []byte("avc1"))
	i = bytes.Index(huge[i+4:], []byte("avc1")) + i + 4
	binary.BigEndian.PutUint16(huge[i+28:], 9000)
	variants = append(variants, huge)
	for n, v := range variants {
		if _, e = mediautil.InspectMP4(bytes.NewReader(v), int64(len(v))); e == nil {
			t.Errorf("accepted malformed variant %d", n)
		}
	}
}
func albumPhoto() object {
	return object{"_": "messageMediaPhoto", "photo": object{"_": "photo", "id": 91, "access_hash": 92, "file_reference": []byte{1}, "date": 100, "sizes": []object{}, "dc_id": 1}}
}
func TestAlbumStableIDsAndAmbiguousPartialUpload(t *testing.T) {
	var ids []int64
	var calls atomic.Int32
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		calls.Add(1)
		switch method {
		case "upload.saveFilePart":
			return object{"_": "boolTrue"}, 200
		case "messages.uploadMedia":
			return albumPhoto(), 200
		case "messages.sendMultiMedia":
			var updates []object
			for i, item := range asObjects(p["multi_media"]) {
				ids = append(ids, item.num("random_id"))
				updates = append(updates, object{"_": "updateMessageID", "id": 30 + i, "random_id": item.num("random_id")})
			}
			return object{"_": "updates", "updates": updates, "users": []object{}, "chats": []object{}, "date": 100, "seq": 1}, 200
		}
		t.Error(method)
		return nil, 500
	})
	mediaClientSource(c, syntheticPNG(t))
	items := []albumItem{{MediaID: "a", Kind: "image"}, {MediaID: "b", Kind: "image"}}
	raw, e := c.sendAlbum(context.Background(), domains.Peer{Type: "user", ID: "42"}, items, "", "1234")
	if e != nil || !json.Valid(raw) || calls.Load() != 5 || len(ids) != 2 || ids[0] == ids[1] || ids[0] != mediaChildRID(1234, 0) || ids[1] != mediaChildRID(1234, 1) {
		t.Fatalf("album result %s err=%v ids=%v calls=%d", raw, e, ids, calls.Load())
	}
	var attempts atomic.Int32
	failed, _ := nativeFixture(t, func(method string, p object) (object, int) {
		switch method {
		case "upload.saveFilePart":
			if attempts.Add(1) == 2 {
				return nil, 500
			}
			return object{"_": "boolTrue"}, 200
		case "messages.uploadMedia":
			return albumPhoto(), 200
		}
		t.Errorf("unexpected final/retry: %s", method)
		return nil, 500
	})
	mediaClientSource(failed, syntheticPNG(t))
	_, e = failed.sendAlbum(context.Background(), domains.Peer{Type: "user", ID: "42"}, items, "", "1234")
	var de *domains.Error
	if !errors.As(e, &de) || !de.Ambiguous || attempts.Load() != 2 {
		t.Fatalf("partial album must stay unknown: %v attempts=%d", e, attempts.Load())
	}
}

func TestMediaStagesPersistBeforeWriteAndStopOnJournalFailure(t *testing.T) {
	for _, failAt := range []int{1, 2, 3, 4, 0} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			var calls atomic.Int32
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				calls.Add(1)
				if method == "upload.saveFilePart" {
					return object{"_": "boolTrue"}, 200
				}
				return object{"_": "updateShortSentMessage", "id": 8, "pts": 1, "pts_count": 1, "date": 100}, 200
			})
			mediaClientSource(c, syntheticPNG(t))
			var stages []domains.OperationStage
			ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error {
				stages = append(stages, s)
				if len(stages) == failAt {
					return errors.New("synthetic journal failure")
				}
				return nil
			})
			_, e := c.Send(ctx, mediaRequest("image"))
			if failAt == 0 {
				if e != nil || len(stages) != 4 || calls.Load() != 2 {
					t.Fatalf("missing stages %v %d %v", stages, calls.Load(), e)
				}
			} else {
				var de *domains.Error
				if e == nil {
					t.Fatal("journal failure ignored")
				}
				if failAt == 1 {
					if calls.Load() != 0 {
						t.Fatal("provider contacted before start persisted")
					}
				} else if !errors.As(e, &de) || !de.Ambiguous {
					t.Fatalf("failure after upload must remain unknown: %v", e)
				}
				if failAt <= 3 && calls.Load() > 1 {
					t.Fatal("final send continued after journal failure")
				}
			}
			for i, s := range stages {
				if s.Number != i/2+1 || s.Nonce != "7812345" {
					t.Fatalf("stage identity mismatch: %#v", s)
				}
				if i%2 == 0 && s.State != "started" {
					t.Fatal("missing start")
				}
				if i%2 == 1 && s.State != "succeeded" {
					t.Fatal("missing completion")
				}
			}
		})
	}
}

func TestUploadRetainsAccountResolvedMegagroupKind(t *testing.T) {
	c, _ := nativeFixture(t, func(method string, p object) (object, int) {
		if method == "upload.saveFilePart" {
			peer := asObject(p["peer"])
			if peer.str("_") != "peerChannel" || peer.num("channel_id") != 91 {
				t.Error("megagroup upload lost channel metadata")
			}
			return object{"_": "boolTrue"}, 200
		}
		t.Error("unexpected provider call")
		return nil, 500
	})
	mediaClientSource(c, []byte("synthetic file"))
	c.session.Peers = map[string]object{"group:channel_91": {"_": "inputPeerChannel", "channel_id": int64(91), "access_hash": int64(777)}}
	_, err := c.uploadMedia(context.Background(), domains.SendRequest{Peer: domains.Peer{Type: "group", ID: "channel_91"}, Kind: "file", MediaID: "owned-local"}, 123)
	if err != nil {
		t.Fatal(err)
	}
}
