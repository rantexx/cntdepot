// Package thumb makes small JPEG previews of photos for screens, so phones do
// not download 4K camera stills just to show a thumbnail.
package thumb

import (
	"bytes"
	"errors"
	"image"
	"image/color"
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

// ErrTooLarge means the image declares more pixels than the hub is willing to
// decode. A small PNG can declare 20000×20000 pixels and cost gigabytes to
// decode, so the size is checked from the header before anything is decoded.
var ErrTooLarge = errors.New("image has too many pixels to scale")

// maxPixels is the most pixels Scale decodes. A 4K still is 8.3 MP and a 50 MP
// phone photo is the upper end of what people really take.
const maxPixels = 50_000_000

// Scale returns a JPEG at most width pixels wide. Images that are already
// narrow enough come back unchanged. Images with more than maxPixels pixels
// are refused with ErrTooLarge without being decoded; callers show the
// original instead, which the browser decodes, not the hub.
func Scale(data []byte, width int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrTooLarge
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

// samples is the most source pixels per axis that feed one preview pixel, so
// at most samples×samples of them are averaged however far an image shrinks.
// That is smooth enough for a thumbnail and keeps the cost per preview pixel
// fixed.
const samples = 4

// step is the distance between samples across a span of source pixels: the
// smallest step that takes at most samples samples, ceil(span/samples).
func step(span int) int {
	return max(1, (span+samples-1)/samples)
}

// rgbFunc returns the colour of the source pixel at x, y in 8 bits a channel.
type rgbFunc func(x, y int) (r, g, b uint8)

// rgbReader returns the fastest way to read src. JPEGs decode to YCbCr, and
// reading those through image.Image would box a color.Color for every sample,
// millions of times for one 4K still. Other images take the generic path.
func rgbReader(src image.Image) rgbFunc {
	if yc, ok := src.(*image.YCbCr); ok {
		return func(x, y int) (uint8, uint8, uint8) {
			c := yc.YCbCrAt(x, y)
			return color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
		}
	}
	return func(x, y int) (uint8, uint8, uint8) {
		r, g, b, _ := src.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
}

// shrink box-averages src down to w×h.
func shrink(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	rgb := rgbReader(src)
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+(y+1)*sh/h
		y1 = max(y1, y0+1)
		ystep := step(y1 - y0)
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+(x+1)*sw/w
			x1 = max(x1, x0+1)
			xstep := step(x1 - x0)
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy += ystep {
				for sx := x0; sx < x1; sx += xstep {
					cr, cg, cb := rgb(sx, sy)
					r, g, bl, n = r+uint32(cr), g+uint32(cg), bl+uint32(cb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n)
			dst.Pix[i+1] = uint8(g / n)
			dst.Pix[i+2] = uint8(bl / n)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}
