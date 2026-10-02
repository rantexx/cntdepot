// Package thumb makes small JPEG previews of photos for screens, so phones do
// not download 4K camera stills just to show a thumbnail.
package thumb

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // phone photos may be PNG
)

// widths are the preview widths the API serves. Anything else is refused, so
// nobody can make the hub do unbounded work.
var widths = []int{160, 320, 640, 1280}

// Allowed reports whether w is a preview width the API serves.
func Allowed(w int) bool {
	for _, x := range widths {
		if x == w {
			return true
		}
	}
	return false
}

// ErrNotImage means the data is not a JPEG or PNG image.
var ErrNotImage = errors.New("not a JPEG or PNG image")

// Scale returns a JPEG at most width pixels wide. Images that are already
// narrow enough come back unchanged.
func Scale(data []byte, width int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= width {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	height := max(1, cfg.Height*width/cfg.Width)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shrink(src, width, height), &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// samples caps how many source pixels per axis feed one preview pixel. A box
// average over at most 4×4 samples looks smooth and keeps a 4K still under
// about 100 ms.
const samples = 4

// shrink box-averages src down to w×h.
func shrink(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+(y+1)*sh/h
		y1 = max(y1, y0+1)
		ystep := max(1, (y1-y0)/samples)
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+(x+1)*sw/w
			x1 = max(x1, x0+1)
			xstep := max(1, (x1-x0)/samples)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy += ystep {
				for sx := x0; sx < x1; sx += xstep {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8((r / n) >> 8)
			dst.Pix[i+1] = uint8((g / n) >> 8)
			dst.Pix[i+2] = uint8((bl / n) >> 8)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}
