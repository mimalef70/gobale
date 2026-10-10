package eitaameow

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"

	"github.com/mimalef70/goomni/src/internal/mediautil"
)

// A separate namespace keeps preview uploads independent of message/album IDs.
// uploadMediaAt journals this derived ID before any part is sent.
func mediaPreviewRID(parent int64) int64 {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(parent))
	sum := sha256.Sum256(append([]byte("peykbridge:eitaa:video-preview:v1:"), raw[:]...))
	id := int64(binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64)
	if id == 0 || id == parent {
		id = parent ^ 1
		if id == 0 {
			id = 2
		}
	}
	return id
}

func videoPreview(ctx context.Context, source io.Reader, size int64) ([]byte, error) {
	r, ok := source.(io.ReadSeeker)
	if !ok {
		return nil, invalidMedia()
	}
	info, err := mediautil.InspectMP4(r, size)
	if err != nil {
		return nil, err
	}
	frame, decodeErr := mediautil.VideoPreview(ctx, r, size, info)
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, invalidMedia()
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if decodeErr != nil {
		// Metadata inspection supports more AVC layouts than the bounded first
		// IDR decoder. Preserve admission without inventing preview pixels.
		return nil, nil
	}
	return mediautil.ThumbnailJPEG(ctx, frame)
}
