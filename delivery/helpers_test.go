package delivery

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"testing"
)

// sampleImage builds a deterministic photo-like test image: smooth
// gradients plus a deterministic pseudo-random texture so lossy
// codecs have real work to do.
func sampleImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			// Smooth deterministic texture, like a real photo's
			// low-frequency content.
			n := int(12 * math.Sin(float64(x)*0.11) * math.Cos(float64(y)*0.07))
			img.Pix[i+0] = uint8((x*255/max(1, w-1) + n) & 0xff)
			img.Pix[i+1] = uint8((y*255/max(1, h-1) + n) & 0xff)
			img.Pix[i+2] = uint8(((x+y)*255/max(1, w+h-2) + n) & 0xff)
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func newTestService(t *testing.T, mutate func(*Config)) (*Service, *SourceStore) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.StatsCapacity = 4096
	if mutate != nil {
		mutate(&cfg)
	}
	store := NewSourceStore()
	return NewService(cfg, store), store
}

// allFormats is a client that accepts everything.
var allFormats = []Format{FormatRIMG, FormatJPEG, FormatPNG}
