package mediautil

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
)

// ThumbnailJPEG derives a bounded preview from decoded pixels.
func ThumbnailJPEG(ctx context.Context, original image.Image) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bounds := original.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if !boundedVideoDimensions(w, h) {
		return nil, invalidMedia()
	}
	tw, th := w, h
	if max(w, h) > 128 {
		tw = max(1, w*128/max(w, h))
		th = max(1, h*128/max(w, h))
	}
	preview := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			preview.Set(x, y, original.At(bounds.Min.X+x*w/tw, bounds.Min.Y+y*h/th))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, preview, &jpeg.Options{Quality: 60}); err != nil {
		return nil, invalidMedia()
	}
	if encoded.Len() > 32<<10 {
		return nil, invalidMedia()
	}
	return encoded.Bytes(), nil
}
