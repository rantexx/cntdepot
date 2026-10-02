package thumb

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
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
