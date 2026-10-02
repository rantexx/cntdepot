package thumb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
	"time"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestScaleShrinksToWidth(t *testing.T) {
	out, err := Scale(testJPEG(t, 2000, 1000), 320)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 160 {
		t.Fatalf("size = %dx%d; want 320x160", b.Dx(), b.Dy())
	}
	r, g, b, _ := img.At(160, 80).RGBA()
	if r>>8 < 170 || g>>8 > 80 || b>>8 > 80 {
		t.Fatalf("color = %d,%d,%d; want about 200,40,40", r>>8, g>>8, b>>8)
	}
}

// redBlue is an image whose left half is red and whose right half is blue.
func redBlue(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 220, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 220, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// TestScaleKeepsWhereColoursAre checks the picture, not just its size: a
// shrink that mixed up samples or channels would still have the right size.
// The JPEG takes the YCbCr fast path and the PNG the generic one.
func TestScaleKeepsWhereColoursAre(t *testing.T) {
	var asJPEG, asPNG bytes.Buffer
	if err := jpeg.Encode(&asJPEG, redBlue(2000, 1000), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&asPNG, redBlue(2000, 1000)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{{"jpeg (YCbCr)", asJPEG.Bytes()}, {"png (generic)", asPNG.Bytes()}} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Scale(tc.data, 640)
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 320 {
				t.Fatalf("size = %dx%d; want 640x320", b.Dx(), b.Dy())
			}
			// Stay clear of the middle, where the JPEG's chroma blurs the edge.
			for _, y := range []int{10, 160, 310} {
				for _, x := range []int{20, 160, 300} {
					r, g, b, _ := img.At(x, y).RGBA()
					if r>>8 < 190 || g>>8 > 40 || b>>8 > 40 {
						t.Errorf("left (%d,%d) = %d,%d,%d; want about red 220,0,0", x, y, r>>8, g>>8, b>>8)
					}
				}
				for _, x := range []int{340, 480, 620} {
					r, g, b, _ := img.At(x, y).RGBA()
					if b>>8 < 190 || r>>8 > 40 || g>>8 > 40 {
						t.Errorf("right (%d,%d) = %d,%d,%d; want about blue 0,0,220", x, y, r>>8, g>>8, b>>8)
					}
				}
			}
		})
	}
}

// TestStepTakesAtMostSamples checks that no span of source pixels is read at
// more than samples points: 3840 to 640 is a span of 6 and used to take 36
// samples a pixel, now at most 4×4.
func TestStepTakesAtMostSamples(t *testing.T) {
	for span := 1; span <= 500; span++ {
		n := 0
		for s := 0; s < span; s += step(span) {
			n++
		}
		if n > samples {
			t.Errorf("span %d takes %d samples; want at most %d", span, n, samples)
		}
	}
	if step(1) != 1 || step(3) != 1 || step(6) != 2 || step(24) != 6 {
		t.Errorf("step(1,3,6,24) = %d,%d,%d,%d; want 1,1,2,6", step(1), step(3), step(6), step(24))
	}
}

// pngDeclaring returns a valid 1×1 PNG whose header claims w×h pixels. Its
// data is a few dozen bytes; decoding it as declared would allocate w×h×4.
func pngDeclaring(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// Signature (8), then the IHDR chunk: length (4), "IHDR" (4), width (4),
	// height (4), 5 more bytes of header, CRC (4) over the type and data.
	const typeAt, widthAt, crcAt = 12, 16, 29
	if string(data[typeAt:typeAt+4]) != "IHDR" {
		t.Fatalf("no IHDR at %d: %q", typeAt, data[typeAt:typeAt+4])
	}
	binary.BigEndian.PutUint32(data[widthAt:], w)
	binary.BigEndian.PutUint32(data[widthAt+4:], h)
	binary.BigEndian.PutUint32(data[crcAt:], crc32.ChecksumIEEE(data[typeAt:crcAt]))
	return data
}

func TestScaleRefusesDecompressionBombs(t *testing.T) {
	for _, tc := range []struct{ w, h uint32 }{
		{20000, 20000},   // 400 MP: about 1.6 GB decoded
		{100000, 100000}, // 10 GP: the product does not fit in 32 bits
	} {
		data := pngDeclaring(t, tc.w, tc.h)
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != int(tc.w) || cfg.Height != int(tc.h) {
			t.Fatalf("test PNG header = %+v, %v; want %dx%d", cfg, err, tc.w, tc.h)
		}
		start := time.Now()
		out, err := Scale(data, 320)
		if !errors.Is(err, ErrTooLarge) || out != nil {
			t.Fatalf("Scale(%dx%d) = %d bytes, %v; want ErrTooLarge", tc.w, tc.h, len(out), err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("Scale(%dx%d) took %v; the size must be checked before decoding", tc.w, tc.h, d)
		}
	}
}

func TestScaleKeepsNarrowImages(t *testing.T) {
	in := testJPEG(t, 300, 200)
	out, err := Scale(in, 320)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("an image narrower than the width must come back unchanged")
	}
}

func TestScaleRejectsNonImages(t *testing.T) {
	if _, err := Scale([]byte("not an image"), 320); !errors.Is(err, ErrNotImage) {
		t.Fatalf("err = %v; want ErrNotImage", err)
	}
}

func TestAllowedWidths(t *testing.T) {
	for _, w := range []int{160, 320, 640, 1280} {
		if !Allowed(w) {
			t.Errorf("Allowed(%d) = false", w)
		}
	}
	for _, w := range []int{0, 100, 4000, -1} {
		if Allowed(w) {
			t.Errorf("Allowed(%d) = true", w)
		}
	}
}

// photoJPEG is a photo-like JPEG: smooth gradients with some texture, so the
// encoder produces realistic 4:2:0 data rather than a flat colour.
func photoJPEG(tb testing.TB, w, h int) []byte {
	tb.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := uint8((x*7+y*13)^(x*y>>3)) & 7
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*255/w) + n, G: uint8(y*255/h) + n, B: uint8((x+y)*255/(w+h)) + n, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

// BenchmarkScale4KTo640 is a truck screen's worst case: one camera still, as
// the hub serves it to a phone.
func BenchmarkScale4KTo640(b *testing.B) {
	data := photoJPEG(b, 3840, 2160)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Scale(data, 640); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkShrink4KTo640 times only the shrink step, without the JPEG decode
// and encode around it.
func BenchmarkShrink4KTo640(b *testing.B) {
	src, err := jpeg.Decode(bytes.NewReader(photoJPEG(b, 3840, 2160)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		shrink(src, 640, 360)
	}
}
