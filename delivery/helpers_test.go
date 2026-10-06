package delivery

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// makeTestImage 在进程内生成确定性的测试图片（渐变+网格图案）。
func makeTestImage(w, h int, seed uint8) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*3 + int(seed)) % 256),
				G: uint8((y*5 + int(seed)*2) % 256),
				B: uint8(((x+y)*2 + int(seed)*3) % 256),
				A: 255,
			})
		}
	}
	return img
}

// encodePNG 把测试图片编码为 PNG 字节。
func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// encodeJPEG 把测试图片编码为 JPEG 字节。
func encodeJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// newTestPipeline 构造带默认配置的测试管线与源图仓库。
func newTestPipeline(t *testing.T, srcID string, srcData []byte) (*Pipeline, *SourceStore, Source) {
	t.Helper()
	store := NewSourceStore()
	src, err := store.Put(srcID, srcData)
	if err != nil {
		t.Fatalf("put source: %v", err)
	}
	p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig()})
	return p, store, src
}
