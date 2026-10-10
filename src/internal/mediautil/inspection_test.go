package mediautil

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func TestNativeVideoPreviewMatchesSyntheticSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/preview.mp4")
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectMP4(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := VideoPreview(context.Background(), bytes.NewReader(raw), int64(len(raw)), info)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Bounds().Dx() != 160 || frame.Bounds().Dy() != 90 || info.DurationMilliseconds != 1000 {
		t.Fatal("incorrect actual frame metadata")
	}
	r, g, b, _ := frame.At(80, 45).RGBA()
	if b < 60000 || r > 3000 || g > 3000 {
		t.Fatal("synthetic blue frame not preserved")
	}
	broken := bytes.Clone(raw)
	i := bytes.Index(broken, []byte("stco"))
	if i < 0 {
		t.Fatal("fixture missing offsets")
	}
	binary.BigEndian.PutUint32(broken[i+12:], uint32(len(raw)-1))
	if _, err = VideoPreview(context.Background(), bytes.NewReader(broken), int64(len(broken)), info); err == nil {
		t.Fatal("out of media sample accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = VideoPreview(ctx, bytes.NewReader(raw), int64(len(raw)), info); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
func TestMP3TimingAndTruncation(t *testing.T) {
	raw, err := os.ReadFile("testdata/audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := InspectMP3(context.Background(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil || ms < 2000 || ms > 2200 {
		t.Fatalf("duration %d %v", ms, err)
	}
	for _, b := range [][]byte{raw[:len(raw)-1], []byte("not audio"), append(bytes.Clone(raw), 0)} {
		if _, err = InspectMP3(context.Background(), bytes.NewReader(b), int64(len(b))); err == nil {
			t.Fatal("invalid MP3 accepted")
		}
	}
}
func FuzzNativeVideoPreview(f *testing.F) {
	raw, _ := os.ReadFile("testdata/preview.mp4")
	f.Add(raw)
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			return
		}
		r := bytes.NewReader(raw)
		info, err := InspectMP4(r, int64(len(raw)))
		if err == nil {
			_, _ = VideoPreview(context.Background(), r, int64(len(raw)), info)
		}
	})
}
func FuzzMP3Inspection(f *testing.F) {
	f.Add([]byte("ID3"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		_, _ = InspectMP3(context.Background(), bytes.NewReader(b), int64(len(b)))
	})
}
