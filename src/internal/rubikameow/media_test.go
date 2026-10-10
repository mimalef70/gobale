package rubikameow

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/mimalef70/goomni/src/domains"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type ownedReader struct{ *bytes.Reader }

func (ownedReader) Close() error { return nil }
func TestMediaStagesBeforeUploadAndSend(t *testing.T) {
	payload := bytes.Repeat([]byte("synthetic"), 40000)
	var parts, send atomic.Int32
	var uploaded bytes.Buffer
	var mu sync.Mutex
	var stages []domains.OperationStage
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("auth") != fixtureAuth || r.Header.Get("file-id") != "123" || r.Header.Get("access-hash-send") != "private-send" {
			t.Error("upload headers")
		}
		body, _ := io.ReadAll(r.Body)
		if strconv.Itoa(len(body)) != r.Header.Get("chunk-size") {
			t.Error("chunk size")
		}
		uploaded.Write(body)
		parts.Add(1)
		_, _ = w.Write([]byte(`{"status":"OK","status_det":"OK","data":{"access_hash_rec":"private-receive"}}`))
	}))
	defer upload.Close()
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		switch method {
		case "requestSendFile":
			mu.Lock()
			defer mu.Unlock()
			if len(stages) != 1 || stages[0].State != "started" {
				t.Error("upload contacted before journal")
			}
			return object{"id": "123", "dc_id": "4", "upload_url": upload.URL, "access_hash_send": "private-send"}
		case "sendMessage":
			mu.Lock()
			defer mu.Unlock()
			if len(stages) != 3 || stages[2].Name != "send" || stages[2].State != "started" {
				t.Error("send contacted before journal")
			}
			if input.str("rnd") != "1234" || asObject(input["file_inline"]).str("access_hash_rec") != "private-receive" {
				t.Error("send media")
			}
			send.Add(1)
			return object{"status": "OK", "message_update": object{"object_guid": "u0peer", "message_id": "42", "message": object{"time": json.Number("1700000000")}}}
		}
		return nil
	})
	c.cfg.MediaSource = func(context.Context, string) (io.ReadCloser, domains.NativeMediaInfo, error) {
		return ownedReader{bytes.NewReader(payload)}, domains.NativeMediaInfo{Name: "sample.bin", Size: int64(len(payload)), ContentType: "application/octet-stream"}, nil
	}
	ctx := domains.WithOperationStageRecorder(context.Background(), func(_ context.Context, s domains.OperationStage) error {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, s)
		return nil
	})
	result, e := c.Send(ctx, domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "u0peer"}, Kind: "file", MediaID: "media-fixture", RequestID: "1234"})
	if e != nil || result.MessageID != "42" {
		t.Fatalf("send result: %+v %v", result, e)
	}
	if !bytes.Equal(uploaded.Bytes(), payload) || parts.Load() != 2 || send.Load() != 1 || len(stages) != 4 || stages[3].State != "succeeded" {
		t.Fatalf("media upload bytes/counts/stages mismatch: %d/%d/%d", parts.Load(), send.Load(), len(stages))
	}
	public, _ := json.Marshal(stages)
	if strings.Contains(string(public), "private-receive") {
		t.Fatal("stage private data leaked")
	}
}
func TestMediaJournalFailurePreventsWire(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRPCFixture(t, func(string, object, bool) object { calls.Add(1); return nil })
	c.cfg.MediaSource = func(context.Context, string) (io.ReadCloser, domains.NativeMediaInfo, error) {
		return ownedReader{bytes.NewReader([]byte("test"))}, domains.NativeMediaInfo{Name: "sample.bin", Size: 4}, nil
	}
	ctx := domains.WithOperationStageRecorder(context.Background(), func(context.Context, domains.OperationStage) error { return errors.New("durable write failed") })
	_, e := c.Send(ctx, domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "u0peer"}, Kind: "file", MediaID: "media-fixture", RequestID: "1234"})
	if e == nil || calls.Load() != 0 {
		t.Fatalf("provider contacted before durable stage: %v %d", e, calls.Load())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDownloadPrivateScopeChunkBoundsAndCancellation(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	payload := bytes.Repeat([]byte("fixture"), 70000)
	var requests atomic.Int32
	c.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() != "https://messenger4.iranlms.ir/GetFile.ashx" || r.Header.Get("access-hash-rec") != "private-receive" {
			t.Error("wrong download target")
		}
		start, _ := strconv.Atoi(r.Header.Get("start-index"))
		last, _ := strconv.Atoi(r.Header.Get("last-index"))
		if last-start+1 > transferChunk || last >= len(payload) {
			t.Fatal("unbounded chunk")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload[start : last+1]))}, nil
	})
	ref, public, e := c.projectMedia(object{"file_id": "123", "dc_id": "4", "access_hash_rec": "private-receive", "size": int64(len(payload)), "file_name": "test.bin", "mime": "bin", "type": "File"})
	if e != nil || public.DownloadSupported {
		t.Fatal("reference advertised before durable storage")
	}
	raw, _ := json.Marshal(ref)
	if strings.Contains(string(raw), "private-receive") {
		t.Fatal("secret exposed")
	}
	reader, e := c.Download(context.Background(), *ref)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(reader)
	if e != nil || !bytes.Equal(body, payload) || requests.Load() != 2 {
		t.Fatalf("download %v count=%d", e, requests.Load())
	}
	reader.Close()
	if _, e = reader.Read(make([]byte, 1)); e == nil {
		t.Fatal("closed stream readable")
	}
	c.mu.Lock()
	c.session.UserID = "u0other"
	c.mu.Unlock()
	if _, e = c.Download(context.Background(), *ref); e == nil {
		t.Fatal("cross-account download accepted")
	}
}

func TestImagePreviewUsesDecodedPixelsAndRejectsTruncation(t *testing.T) {
	original := image.NewRGBA(image.Rect(0, 0, 320, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 320; x++ {
			original.Set(x, y, color.RGBA{20, 80, 180, 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, original); err != nil {
		t.Fatal(err)
	}
	preview, err := imageThumbnail(context.Background(), bytes.NewReader(source.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(preview)
	if err != nil {
		t.Fatal(err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "jpeg" || decoded.Bounds().Dx() != 128 || decoded.Bounds().Dy() != 64 {
		t.Fatal("invalid actual preview")
	}
	r, g, b, _ := decoded.At(64, 32).RGBA()
	if r >= g || g >= b {
		t.Fatal("preview did not preserve source pixels")
	}
	if _, err = imageThumbnail(context.Background(), bytes.NewReader(source.Bytes()[:len(source.Bytes())/2])); err == nil {
		t.Fatal("truncated image accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = imageThumbnail(ctx, bytes.NewReader(source.Bytes())); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled preview decoded")
	}
}
