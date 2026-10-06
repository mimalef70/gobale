package balemeow

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// These limits bound the parser independently of the gateway's file-size limit.
// Only ordinary mono/stereo Ogg Opus recordings are supported, without chained
// streams, leading junk, cropping offsets or trailing data. No codec executes.
const maxOpusPacketBytes = 64 << 10

var errInvalidVoice = errors.New("invalid Ogg Opus recording")

type voiceContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r voiceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// prepareVoiceSource inspects before the first provider upload RPC. Files from
// the immutable media store are scanned and rewound; non-seekable adapters use a
// private temporary file, never an unbounded in-memory copy.
func prepareVoiceSource(ctx context.Context, source io.Reader, size int64) (io.Reader, int32, func(), error) {
	cleanup := func() {}
	var rewind io.ReadSeeker
	var reader io.Reader
	if rs, ok := source.(io.ReadSeeker); ok {
		rewind = rs
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return nil, 0, cleanup, boundedError("INVALID_MEDIA", "voice source cannot be read", 400)
		}
		reader = rs
	} else {
		f, err := os.CreateTemp("", "gobale-voice-*.ogg")
		if err != nil {
			return nil, 0, cleanup, boundedError("MEDIA_TEMP_UNAVAILABLE", "voice inspection temporary storage is unavailable", 503)
		}
		cleanup = func() { _ = f.Close(); _ = os.Remove(f.Name()) }
		rewind = f
		reader = io.TeeReader(source, f)
	}
	// One extra byte detects inaccurate metadata and concatenated/trailing data.
	duration, err := inspectOggOpus(voiceContextReader{ctx, io.LimitReader(reader, size+1)}, size)
	if err != nil {
		cleanup()
		if ctx.Err() != nil {
			return nil, 0, func() {}, ctx.Err()
		}
		return nil, 0, func() {}, boundedError("INVALID_MEDIA", "voice must be a complete mono/stereo Ogg Opus recording with valid framing, duration and checksums", 400)
	}
	if _, err = rewind.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, func() {}, boundedError("INVALID_MEDIA", "voice source cannot be rewound", 400)
	}
	return rewind, duration, cleanup, nil
}

// inspectOggOpus follows RFC 7845's 48 kHz granule clock and pre-skip, and RFC
// 6716 packet framing. Granules are checked against actual packet sample counts,
// so arbitrary metadata cannot invent a long duration. It validates containers
// and compressed packet framing, not the decoded waveform or speech content.
func inspectOggOpus(r io.Reader, size int64) (int32, error) {
	if size <= 0 || size > math.MaxInt32 {
		return 0, errInvalidVoice
	}
	var consumed int64
	var serial, nextSequence uint32
	var packet []byte
	var packets int
	var preSkip, decoded, lastGranule, lastSamples uint64
	var audioPackets int
	var eos bool
	for !eos {
		var header [27]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return 0, errInvalidVoice
		}
		consumed += 27
		if string(header[:4]) != "OggS" || header[4] != 0 || header[5]&^byte(7) != 0 || header[26] == 0 {
			return 0, errInvalidVoice
		}
		flags := header[5]
		if (flags&1 != 0) != (len(packet) != 0) {
			return 0, errInvalidVoice
		}
		pageSerial := binary.LittleEndian.Uint32(header[14:18])
		sequence := binary.LittleEndian.Uint32(header[18:22])
		if consumed == 27 {
			if flags != 2 || sequence != 0 {
				return 0, errInvalidVoice
			}
			serial = pageSerial
		} else if flags&2 != 0 || pageSerial != serial {
			return 0, errInvalidVoice
		}
		if sequence != nextSequence || nextSequence == math.MaxUint32 {
			return 0, errInvalidVoice
		}
		nextSequence++
		lacing := make([]byte, int(header[26]))
		if _, err := io.ReadFull(r, lacing); err != nil {
			return 0, errInvalidVoice
		}
		pageSize := 0
		for _, n := range lacing {
			pageSize += int(n)
		}
		consumed += int64(len(lacing) + pageSize)
		if consumed > size {
			return 0, errInvalidVoice
		}
		body := make([]byte, pageSize)
		if _, err := io.ReadFull(r, body); err != nil {
			return 0, errInvalidVoice
		}
		crc := binary.LittleEndian.Uint32(header[22:26])
		clear(header[22:26])
		if oggCRC(oggCRC(oggCRC(0, header[:]), lacing), body) != crc {
			return 0, errInvalidVoice
		}
		completed, audioCompleted, off := 0, 0, 0
		for _, length := range lacing {
			if len(packet)+int(length) > maxOpusPacketBytes {
				return 0, errInvalidVoice
			}
			packet = append(packet, body[off:off+int(length)]...)
			off += int(length)
			if length == 255 {
				continue
			}
			switch packets {
			case 0:
				if len(packet) != 19 || !bytes.Equal(packet[:8], []byte("OpusHead")) || packet[8] != 1 || (packet[9] != 1 && packet[9] != 2) || packet[18] != 0 || len(lacing) != 1 || nextSequence != 1 {
					return 0, errInvalidVoice
				}
				preSkip = uint64(binary.LittleEndian.Uint16(packet[10:12]))
			case 1:
				if !validOpusTags(packet) {
					return 0, errInvalidVoice
				}
			default:
				samples, err := opusPacketSamples(packet)
				if err != nil {
					return 0, err
				}
				decoded += samples
				lastSamples = samples
				audioPackets++
				audioCompleted++
			}
			packets++
			completed++
			packet = packet[:0]
		}
		granule := binary.LittleEndian.Uint64(header[6:14])
		eos = flags&4 != 0
		switch {
		case completed == 0 && packets >= 2:
			if granule != math.MaxUint64 || eos {
				return 0, errInvalidVoice
			}
		case audioPackets == 0:
			if granule != 0 || eos {
				return 0, errInvalidVoice
			}
		case completed == 0:
			if granule != math.MaxUint64 || eos {
				return 0, errInvalidVoice
			}
		case audioCompleted == 0:
			return 0, errInvalidVoice
		default:
			if granule > decoded || granule < lastGranule || (!eos && granule != decoded) || (eos && (len(packet) != 0 || decoded-granule >= lastSamples)) {
				return 0, errInvalidVoice
			}
			lastGranule = granule
		}
	}
	if consumed != size || lastGranule <= preSkip {
		return 0, errInvalidVoice
	}
	var extra [1]byte
	if n, err := io.ReadFull(r, extra[:]); n != 0 || err != io.EOF {
		return 0, errInvalidVoice
	}
	milliseconds := (lastGranule - preSkip) / 48
	if milliseconds == 0 || milliseconds > math.MaxInt32 {
		return 0, errInvalidVoice
	}
	return int32(milliseconds), nil
}

func validOpusTags(p []byte) bool {
	if len(p) < 16 || string(p[:8]) != "OpusTags" {
		return false
	}
	vendor := uint64(binary.LittleEndian.Uint32(p[8:12]))
	if vendor > uint64(len(p)-16) {
		return false
	}
	p = p[12+int(vendor):]
	count := binary.LittleEndian.Uint32(p[:4])
	p = p[4:]
	if uint64(count) > uint64(len(p)/4) {
		return false
	}
	for i := uint32(0); i < count; i++ {
		if len(p) < 4 {
			return false
		}
		length := uint64(binary.LittleEndian.Uint32(p[:4]))
		p = p[4:]
		if length > uint64(len(p)) {
			return false
		}
		p = p[int(length):]
	}
	return true // RFC 7845 permits padding after the comment fields.
}

// opusPacketSamples validates all frame-length declarations while keeping the
// compressed frame bytes opaque. One packet may represent at most 120 ms.
func opusPacketSamples(p []byte) (uint64, error) {
	if len(p) == 0 {
		return 0, errInvalidVoice
	}
	config := p[0] >> 3
	var perFrame int
	switch {
	case config < 12:
		perFrame = []int{480, 960, 1920, 2880}[config&3]
	case config < 16:
		perFrame = []int{480, 960}[config&1]
	default:
		perFrame = []int{120, 240, 480, 960}[config&3]
	}
	code := p[0] & 3
	p = p[1:]
	frames := 1
	readSize := func() (int, bool) {
		if len(p) == 0 {
			return 0, false
		}
		n := int(p[0])
		p = p[1:]
		if n >= 252 {
			if len(p) == 0 {
				return 0, false
			}
			n += 4 * int(p[0])
			p = p[1:]
		}
		return n, n <= 1275
	}
	switch code {
	case 0:
		if len(p) > 1275 {
			return 0, errInvalidVoice
		}
	case 1:
		frames = 2
		if len(p)%2 != 0 || len(p)/2 > 1275 {
			return 0, errInvalidVoice
		}
	case 2:
		frames = 2
		n, ok := readSize()
		if !ok || n > len(p) || len(p)-n > 1275 {
			return 0, errInvalidVoice
		}
	case 3:
		if len(p) == 0 {
			return 0, errInvalidVoice
		}
		control := p[0]
		p = p[1:]
		frames = int(control & 63)
		if frames == 0 || frames*perFrame > 5760 {
			return 0, errInvalidVoice
		}
		if control&64 != 0 {
			padding := 0
			for {
				if len(p) == 0 {
					return 0, errInvalidVoice
				}
				n := int(p[0])
				p = p[1:]
				if n == 255 {
					padding += 254
				} else {
					padding += n
					break
				}
			}
			if padding > len(p) {
				return 0, errInvalidVoice
			}
			p = p[:len(p)-padding]
		}
		if control&128 == 0 {
			if len(p)%frames != 0 || len(p)/frames > 1275 {
				return 0, errInvalidVoice
			}
		} else {
			total := 0
			for i := 1; i < frames; i++ {
				n, ok := readSize()
				if !ok {
					return 0, errInvalidVoice
				}
				total += n
			}
			if total > len(p) || len(p)-total > 1275 {
				return 0, errInvalidVoice
			}
		}
	}
	if frames*perFrame > 5760 {
		return 0, errInvalidVoice
	}
	return uint64(frames * perFrame), nil
}

var oggCRCTable = func() [256]uint32 {
	var table [256]uint32
	for i := range table {
		x := uint32(i) << 24
		for range 8 {
			if x&0x80000000 != 0 {
				x = (x << 1) ^ 0x04c11db7
			} else {
				x <<= 1
			}
		}
		table[i] = x
	}
	return table
}()

func oggCRC(crc uint32, p []byte) uint32 {
	for _, b := range p {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}
