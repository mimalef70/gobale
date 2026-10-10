package mediautil

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"io"

	"github.com/Eyevinn/hi264/pkg/decoder"
	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
)

// VideoPreview decodes the first AVC IDR access unit, without reading the whole
// media body. Fragmented MP4, non-IDR first samples and other codecs are rejected.
func VideoPreview(ctx context.Context, r io.ReadSeeker, size int64, info VideoInfo) (out image.Image, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = invalidMedia()
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	roots, e := mp4Boxes(r, 0, size)
	if e != nil {
		return nil, invalidMedia()
	}
	moov, ok := mp4Unique(roots, "moov")
	if !ok {
		return nil, invalidMedia()
	}
	children, e := mp4Boxes(r, moov.start, moov.end)
	if e != nil {
		return nil, invalidMedia()
	}
	child := func(parent mp4Box, name string) (mp4Box, bool) {
		a, e := mp4Boxes(r, parent.start, parent.end)
		if e != nil {
			return mp4Box{}, false
		}
		return mp4Unique(a, name)
	}
	for _, track := range children {
		if track.kind != "trak" {
			continue
		}
		mdia, ok := child(track, "mdia")
		if !ok {
			continue
		}
		hdlr, ok := child(mdia, "hdlr")
		if !ok {
			continue
		}
		h, e := mp4Read(r, hdlr, 64<<10)
		if e != nil || len(h) < 12 || string(h[8:12]) != "vide" {
			continue
		}
		minf, ok := child(mdia, "minf")
		if !ok {
			return nil, invalidMedia()
		}
		stbl, ok := child(minf, "stbl")
		if !ok {
			return nil, invalidMedia()
		}
		table, e := mp4Boxes(r, stbl.start, stbl.end)
		if e != nil {
			return nil, invalidMedia()
		}
		read := func(name string) ([]byte, error) {
			b, ok := mp4Unique(table, name)
			if !ok {
				return nil, invalidMedia()
			}
			return mp4Read(r, b, 1<<20)
		}
		sd, e := read("stsd")
		if e != nil || len(sd) < 8 {
			return nil, invalidMedia()
		}
		entries, e := mp4Boxes(bytes.NewReader(sd), 8, int64(len(sd)))
		if e != nil || len(entries) != 1 || entries[0].kind != "avc1" {
			return nil, invalidMedia()
		}
		config, e := mp4Boxes(bytes.NewReader(sd), entries[0].start+78, entries[0].end)
		if e != nil {
			return nil, invalidMedia()
		}
		b, ok := mp4Unique(config, "avcC")
		if !ok {
			return nil, invalidMedia()
		}
		raw := sd[b.start:b.end]
		if len(raw) < 7 || raw[0] != 1 {
			return nil, invalidMedia()
		}
		lengthBytes := int(raw[4]&3) + 1
		var nalus [][]byte
		off := 6
		counts := []int{int(raw[5] & 31)}
		for section := 0; section < 2; section++ {
			if section == 1 {
				if off >= len(raw) {
					return nil, invalidMedia()
				}
				counts = append(counts, int(raw[off]))
				off++
			}
			count := counts[section]
			if count < 1 || count > 32 {
				return nil, invalidMedia()
			}
			for i := 0; i < count; i++ {
				if off+2 > len(raw) {
					return nil, invalidMedia()
				}
				n := int(binary.BigEndian.Uint16(raw[off:]))
				off += 2
				if n < 1 || off+n > len(raw) {
					return nil, invalidMedia()
				}
				v := raw[off : off+n]
				off += n
				if section == 0 {
					sps, e := avc.ParseSPSNALUnit(v, true)
					if e != nil || !boundedVideoDimensions(int(sps.Width), int(sps.Height)) || int(sps.Width) != info.Width || int(sps.Height) != info.Height {
						return nil, invalidMedia()
					}
				}
				nalus = append(nalus, v)
			}
		}
		sz, e := read("stsz")
		if e != nil || len(sz) < 12 || sz[0] != 0 || binary.BigEndian.Uint32(sz[8:]) == 0 {
			return nil, invalidMedia()
		}
		n := int64(binary.BigEndian.Uint32(sz[4:]))
		if n == 0 {
			if len(sz) < 16 {
				return nil, invalidMedia()
			}
			n = int64(binary.BigEndian.Uint32(sz[12:]))
		}
		if n < 1 || n > 4<<20 {
			return nil, invalidMedia()
		}
		sc, e := read("stsc")
		if e != nil || len(sc) < 20 || binary.BigEndian.Uint32(sc[4:]) == 0 || binary.BigEndian.Uint32(sc[8:]) != 1 || binary.BigEndian.Uint32(sc[12:]) == 0 || binary.BigEndian.Uint32(sc[16:]) != 1 {
			return nil, invalidMedia()
		}
		var pos uint64
		co, e := read("stco")
		if e == nil {
			if len(co) < 12 || co[0] != 0 || binary.BigEndian.Uint32(co[4:]) == 0 {
				return nil, invalidMedia()
			}
			pos = uint64(binary.BigEndian.Uint32(co[8:]))
		} else {
			co, e = read("co64")
			if e != nil || len(co) < 16 || co[0] != 0 || binary.BigEndian.Uint32(co[4:]) == 0 {
				return nil, invalidMedia()
			}
			pos = binary.BigEndian.Uint64(co[8:])
		}
		if pos > uint64(size) || uint64(n) > uint64(size)-pos {
			return nil, invalidMedia()
		}
		inMedia := false
		for _, root := range roots {
			if root.kind == "mdat" && int64(pos) >= root.start && int64(pos)+n <= root.end {
				inMedia = true
			}
		}
		if !inMedia {
			return nil, invalidMedia()
		}
		if _, e = r.Seek(int64(pos), io.SeekStart); e != nil {
			return nil, invalidMedia()
		}
		sample := make([]byte, n)
		if _, e = io.ReadFull(r, sample); e != nil {
			return nil, invalidMedia()
		}
		idr := false
		for off := 0; off < len(sample); {
			if len(nalus) >= 96 || off+lengthBytes > len(sample) {
				return nil, invalidMedia()
			}
			length := uint32(0)
			for j := 0; j < lengthBytes; j++ {
				length = length<<8 | uint32(sample[off+j])
			}
			off += lengthBytes
			if length < 1 || uint64(length) > uint64(len(sample)-off) {
				return nil, invalidMedia()
			}
			v := sample[off : off+int(length)]
			off += int(length)
			switch v[0] & 31 {
			case 5:
				idr = true
			case 1, 2, 3, 4, 7, 8:
				return nil, invalidMedia()
			}
			nalus = append(nalus, v)
		}
		if !idr {
			return nil, invalidMedia()
		}
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		frame, e := decoder.New().DecodeNALUs(nalus)
		if e != nil || frame.Width != info.Width || frame.Height != info.Height {
			return nil, invalidMedia()
		}
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		return yuv.FrameToImage(frame), nil
	}
	return nil, invalidMedia()
}
