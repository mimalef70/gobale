package mediautil

import (
	"context"
	"github.com/mimalef70/goomni/src/domains"
	"io"
	"os"
)

type mediaContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r mediaContextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func SeekableSource(ctx context.Context, source io.Reader, size int64) (io.ReadSeeker, func(), error) {
	done := func() {}
	if r, ok := source.(io.ReadSeeker); ok {
		end, e := r.Seek(0, io.SeekEnd)
		if e != nil || end != size {
			return nil, done, domains.E("INVALID_MEDIA", "media source size or position is invalid", 400)
		}
		if _, e = r.Seek(0, io.SeekStart); e != nil {
			return nil, done, domains.E("INVALID_MEDIA", "media source size or position is invalid", 400)
		}
		return r, done, nil
	}
	f, e := os.CreateTemp("", "goomni-media-inspect-*")
	if e != nil {
		return nil, done, domains.E("MEDIA_TEMP_UNAVAILABLE", "media inspection temporary storage is unavailable", 503)
	}
	done = func() { _ = f.Close(); _ = os.Remove(f.Name()) }
	n, e := io.Copy(f, io.LimitReader(mediaContextReader{ctx, source}, size+1))
	if e != nil || n != size {
		done()
		if ctx.Err() != nil {
			return nil, func() {}, ctx.Err()
		}
		return nil, func() {}, domains.E("INVALID_MEDIA", "media source size or position is invalid", 400)
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		done()
		return nil, func() {}, domains.E("INVALID_MEDIA", "media source size or position is invalid", 400)
	}
	return f, done, nil
}
