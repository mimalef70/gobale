package mediautil

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"

	"github.com/mimalef70/goomni/src/domains"
)

func invalidMedia() error {
	return domains.E("INVALID_MEDIA", "video container metadata is invalid or unsupported", 400)
}
func boundedVideoDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= 8192 && h <= 8192 && int64(w)*int64(h) <= 16777216
}

// PrepareVideoSource inspects and rewinds a bounded source before upload.
func PrepareVideoSource(ctx context.Context, source io.Reader, size int64) (io.Reader, VideoInfo, func(), error) {
	r, done, err := SeekableSource(ctx, source, size)
	if err != nil {
		return nil, VideoInfo{}, done, err
	}
	info, err := InspectMP4(r, size)
	if err != nil {
		done()
		return nil, VideoInfo{}, func() {}, err
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, VideoInfo{}, func() {}, invalidMedia()
	}
	return r, info, done, nil
}

// Bounded ISO-BMFF metadata inspection: ordinary unfragmented AVC, one video
// track, sample timing matching its media header, actual sample-entry dimensions.
// Media bytes are skipped by seeking; this does not decode H.264 content.
type VideoInfo struct {
	DurationSeconds      int64
	DurationMilliseconds int64
	Width, Height        int
}
type mp4Box struct {
	kind       string
	start, end int64
}

func mp4Boxes(r io.ReadSeeker, start, end int64) ([]mp4Box, error) {
	var boxes []mp4Box
	for off := start; off < end; {
		if len(boxes) >= 4096 || end-off < 8 {
			return nil, invalidMedia()
		}
		if _, e := r.Seek(off, io.SeekStart); e != nil {
			return nil, e
		}
		var h [16]byte
		if _, e := io.ReadFull(r, h[:8]); e != nil {
			return nil, e
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		header := int64(8)
		if size == 1 {
			if end-off < 16 {
				return nil, invalidMedia()
			}
			if _, e := io.ReadFull(r, h[8:]); e != nil {
				return nil, e
			}
			n := binary.BigEndian.Uint64(h[8:])
			if n > math.MaxInt64 {
				return nil, invalidMedia()
			}
			size = int64(n)
			header = 16
		}
		if size == 0 {
			size = end - off
		}
		if size < header || size > end-off {
			return nil, invalidMedia()
		}
		boxes = append(boxes, mp4Box{string(h[4:8]), off + header, off + size})
		off += size
	}
	return boxes, nil
}
func mp4Read(r io.ReadSeeker, b mp4Box, maxBytes int64) ([]byte, error) {
	if b.end-b.start > maxBytes {
		return nil, invalidMedia()
	}
	if _, e := r.Seek(b.start, io.SeekStart); e != nil {
		return nil, e
	}
	p := make([]byte, int(b.end-b.start))
	_, e := io.ReadFull(r, p)
	return p, e
}
func mp4Unique(boxes []mp4Box, kind string) (mp4Box, bool) {
	var result mp4Box
	found := false
	for _, b := range boxes {
		if b.kind == kind {
			if found {
				return mp4Box{}, false
			}
			result = b
			found = true
		}
	}
	return result, found
}
func InspectMP4(r io.ReadSeeker, size int64) (VideoInfo, error) {
	bad := func() (VideoInfo, error) { return VideoInfo{}, invalidMedia() }
	boxes, e := mp4Boxes(r, 0, size)
	if e != nil {
		return bad()
	}
	ftyp, ok := mp4Unique(boxes, "ftyp")
	if !ok {
		return bad()
	}
	f, e := mp4Read(r, ftyp, 256)
	if e != nil || len(f) < 8 || len(f)%4 != 0 {
		return bad()
	}
	brand := false
	for i := 0; i+4 <= len(f); i += 4 {
		if i == 4 {
			continue
		}
		switch string(f[i : i+4]) {
		case "isom", "iso2", "mp41", "mp42", "avc1":
			brand = true
		}
	}
	if !brand {
		return bad()
	}
	moov, ok := mp4Unique(boxes, "moov")
	if !ok || moov.end-moov.start > 4<<20 {
		return bad()
	}
	hasData := false
	for _, b := range boxes {
		if b.kind == "moof" {
			return bad()
		}
		if b.kind == "mdat" && b.end > b.start {
			hasData = true
		}
	}
	if !hasData {
		return bad()
	}
	children, e := mp4Boxes(r, moov.start, moov.end)
	if e != nil {
		return bad()
	}
	var result VideoInfo
	videos := 0
	tracks := 0
	for _, track := range children {
		if track.kind == "mvex" {
			return bad()
		}
		if track.kind != "trak" {
			continue
		}
		tracks++
		if tracks > 16 {
			return bad()
		}
		tb, e := mp4Boxes(r, track.start, track.end)
		if e != nil {
			return bad()
		}
		mdia, ok := mp4Unique(tb, "mdia")
		if !ok {
			return bad()
		}
		mb, e := mp4Boxes(r, mdia.start, mdia.end)
		if e != nil {
			return bad()
		}
		handler, ok := mp4Unique(mb, "hdlr")
		if !ok {
			return bad()
		}
		h, e := mp4Read(r, handler, 64<<10)
		if e != nil || len(h) < 12 {
			return bad()
		}
		if string(h[8:12]) != "vide" {
			continue
		}
		videos++
		if videos > 1 {
			return bad()
		}
		mdhd, ok := mp4Unique(mb, "mdhd")
		if !ok {
			return bad()
		}
		m, e := mp4Read(r, mdhd, 64)
		if e != nil || len(m) < 24 {
			return bad()
		}
		var scale, duration uint64
		switch m[0] {
		case 0:
			scale = uint64(binary.BigEndian.Uint32(m[12:16]))
			duration = uint64(binary.BigEndian.Uint32(m[16:20]))
		case 1:
			if len(m) < 36 {
				return bad()
			}
			scale = uint64(binary.BigEndian.Uint32(m[20:24]))
			duration = binary.BigEndian.Uint64(m[24:32])
		default:
			return bad()
		}
		if scale == 0 || duration == 0 || duration > scale*math.MaxInt32 {
			return bad()
		}
		minf, ok := mp4Unique(mb, "minf")
		if !ok {
			return bad()
		}
		ib, e := mp4Boxes(r, minf.start, minf.end)
		if e != nil {
			return bad()
		}
		stbl, ok := mp4Unique(ib, "stbl")
		if !ok {
			return bad()
		}
		sb, e := mp4Boxes(r, stbl.start, stbl.end)
		if e != nil {
			return bad()
		}
		stsd, ok := mp4Unique(sb, "stsd")
		if !ok {
			return bad()
		}
		sd, e := mp4Read(r, stsd, 1<<20)
		if e != nil || len(sd) < 8 || sd[0] != 0 || binary.BigEndian.Uint32(sd[4:8]) != 1 {
			return bad()
		}
		entries, e := mp4Boxes(bytes.NewReader(sd), 8, int64(len(sd)))
		if e != nil || len(entries) != 1 || entries[0].kind != "avc1" {
			return bad()
		}
		entry := entries[0]
		if entry.end-entry.start < 78 {
			return bad()
		}
		off := int(entry.start)
		width := int(binary.BigEndian.Uint16(sd[off+24 : off+26]))
		height := int(binary.BigEndian.Uint16(sd[off+26 : off+28]))
		if !boundedVideoDimensions(width, height) {
			return bad()
		}
		configs, e := mp4Boxes(bytes.NewReader(sd), entry.start+78, entry.end)
		if e != nil {
			return bad()
		}
		avcc, ok := mp4Unique(configs, "avcC")
		if !ok || avcc.end-avcc.start < 7 || sd[avcc.start] != 1 {
			return bad()
		}
		stts, ok := mp4Unique(sb, "stts")
		if !ok {
			return bad()
		}
		ts, e := mp4Read(r, stts, 1<<20)
		if e != nil || len(ts) < 8 || ts[0] != 0 {
			return bad()
		}
		count := int(binary.BigEndian.Uint32(ts[4:8]))
		if count < 1 || count > 100000 || len(ts) != 8+count*8 {
			return bad()
		}
		var ticks uint64
		for i := 0; i < count; i++ {
			n := uint64(binary.BigEndian.Uint32(ts[8+i*8:]))
			d := uint64(binary.BigEndian.Uint32(ts[12+i*8:]))
			if n == 0 || d == 0 || n > duration/d || ticks > duration-n*d {
				return bad()
			}
			ticks += n * d
		}
		if ticks != duration {
			return bad()
		}
		result = VideoInfo{DurationSeconds: int64((duration + scale - 1) / scale), DurationMilliseconds: int64(duration/scale)*1000 + int64((duration%scale)*1000/scale), Width: width, Height: height}
	}
	if videos != 1 {
		return bad()
	}
	return result, nil
}
