package delivery

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
)

// jpegCodec is the universal fallback codec.
type jpegCodec struct{}

func (jpegCodec) Format() Format { return FormatJPEG }

func (jpegCodec) Encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, toRGBA(img), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (jpegCodec) Decode(data []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(data))
}

// pngCodec is the lossless codec.
type pngCodec struct{}

func (pngCodec) Format() Format { return FormatPNG }

func (pngCodec) Encode(img image.Image, _ int) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, toRGBA(img)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (pngCodec) Decode(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

// toRGBA converts any image to *image.RGBA deterministically.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func init() {
	registerCodec(jpegCodec{})
	registerCodec(pngCodec{})
}
