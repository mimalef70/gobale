package mediautil

import (
	"context"
	"encoding/binary"
	"io"
)

// InspectMP3 derives duration from a complete sequence of MPEG layer III frames.
// ID3 tags are skipped under a byte bound; no caller duration is trusted.
func InspectMP3(ctx context.Context, r io.ReadSeeker, size int64) (int64, error) {
	if size < 4 {
		return 0, invalidMedia()
	}
	var head [10]byte
	if _, e := r.Seek(0, io.SeekStart); e != nil {
		return 0, invalidMedia()
	}
	if _, e := io.ReadFull(r, head[:min(size, 10)]); e != nil {
		return 0, invalidMedia()
	}
	off := int64(0)
	if string(head[:3]) == "ID3" {
		if size < 10 || head[3] < 2 || head[3] > 4 || head[4] != 0 || head[5]&0x0f != 0 {
			return 0, invalidMedia()
		}
		tag := int64(0)
		for _, v := range head[6:10] {
			if v&128 != 0 {
				return 0, invalidMedia()
			}
			tag = tag<<7 | int64(v)
		}
		if tag > 1<<20 || tag > size-10 {
			return 0, invalidMedia()
		}
		off = 10 + tag
		if head[3] == 4 && head[5]&16 != 0 {
			off += 10
		}
	}
	end := size
	if size-off >= 128 {
		if _, e := r.Seek(size-128, io.SeekStart); e != nil {
			return 0, invalidMedia()
		}
		if _, e := io.ReadFull(r, head[:3]); e != nil {
			return 0, invalidMedia()
		}
		if string(head[:3]) == "TAG" {
			end -= 128
		}
	}
	var samples int64
	sampleRate, version := -1, -1
	frames := 0
	high := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	low := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	for off < end {
		if e := ctx.Err(); e != nil {
			return 0, e
		}
		if frames >= 2000000 || end-off < 4 {
			return 0, invalidMedia()
		}
		if _, e := r.Seek(off, io.SeekStart); e != nil {
			return 0, invalidMedia()
		}
		if _, e := io.ReadFull(r, head[:4]); e != nil {
			return 0, invalidMedia()
		}
		h := binary.BigEndian.Uint32(head[:4])
		v := int(h >> 19 & 3)
		layer := h >> 17 & 3
		bi := int(h >> 12 & 15)
		si := int(h >> 10 & 3)
		if h>>21 != 0x7ff || v == 1 || layer != 1 || bi == 0 || bi == 15 || si == 3 || h&3 == 2 {
			return 0, invalidMedia()
		}
		rate := [3]int{44100, 48000, 32000}[si]
		bitrate := high[bi]
		factor, count := 144, 1152
		if v != 3 {
			bitrate = low[bi]
			factor, count = 72, 576
			if v == 2 {
				rate /= 2
			} else {
				rate /= 4
			}
		}
		if frames == 0 {
			sampleRate, version = rate, v
		} else if sampleRate != rate || version != v {
			return 0, invalidMedia()
		}
		length := int64(factor*bitrate*1000/rate + int(h>>9&1))
		if length < 4 || length > end-off {
			return 0, invalidMedia()
		}
		samples += int64(count)
		frames++
		off += length
	}
	if frames < 2 || samples == 0 {
		return 0, invalidMedia()
	}
	if _, e := r.Seek(0, io.SeekStart); e != nil {
		return 0, invalidMedia()
	}
	return samples * 1000 / int64(sampleRate), nil
}
