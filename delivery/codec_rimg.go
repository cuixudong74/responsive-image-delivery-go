package delivery

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

// rimgMagic identifies the in-process modern format.
const rimgMagic = "RIMG"

// rimgCodec is a compact deterministic lossy codec implemented fully
// in process: RGB -> YCbCr, 2x2 chroma subsampling, uniform
// quantization controlled by quality, then flate compression of the
// planes. Decoding is only possible in this process, which matches
// the in-process delivery scope. Given the same image and quality the
// output is byte-identical across runs and goroutine counts.
type rimgCodec struct{}

func (rimgCodec) Format() Format { return FormatRIMG }

// rimgShifts maps quality (1..100) to quantization shifts.
func rimgShifts(quality int) (shiftY, shiftC uint8) {
	if quality < 1 {
		quality = 1
	}
	if quality > 100 {
		quality = 100
	}
	s := (100-quality)/20 + 1 // 1..4
	if s > 3 {
		s = 3
	}
	sc := s + 1
	if sc > 4 {
		sc = 4
	}
	return uint8(s), uint8(sc)
}

func (rimgCodec) Encode(img image.Image, quality int) ([]byte, error) {
	rgba := toRGBA(img)
	w, h := rgba.Rect.Dx(), rgba.Rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, newError(ReasonGenerationFailed, "rimg: empty image")
	}
	shiftY, shiftC := rimgShifts(quality)
	cw, ch := (w+1)/2, (h+1)/2
	yPlane := make([]byte, w*h)
	cbPlane := make([]byte, cw*ch)
	crPlane := make([]byte, cw*ch)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := rgba.PixOffset(x, y)
			yy, cb, cr := color.RGBToYCbCr(rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2])
			yPlane[y*w+x] = yy >> shiftY
			if x%2 == 0 && y%2 == 0 {
				cbPlane[(y/2)*cw+(x/2)] = cb >> shiftC
				crPlane[(y/2)*cw+(x/2)] = cr >> shiftC
			}
		}
	}
	// Left-predictor delta coding turns smooth gradients into long
	// runs of small deltas, which flate compresses well.
	deltaCode(yPlane, w)
	deltaCode(cbPlane, cw)
	deltaCode(crPlane, cw)
	var buf bytes.Buffer
	buf.WriteString(rimgMagic)
	var hdr [12]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(hdr[4:8], uint32(h))
	hdr[8] = shiftY
	hdr[9] = shiftC
	buf.Write(hdr[:10])
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	for _, plane := range [][]byte{yPlane, cbPlane, crPlane} {
		if _, err := fw.Write(plane); err != nil {
			return nil, err
		}
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (rimgCodec) Decode(data []byte) (image.Image, error) {
	if len(data) < 14 || string(data[:4]) != rimgMagic {
		return nil, newError(ReasonGenerationFailed, "rimg: bad magic")
	}
	w := int(binary.BigEndian.Uint32(data[4:8]))
	h := int(binary.BigEndian.Uint32(data[8:12]))
	shiftY, shiftC := data[12], data[13]
	if w <= 0 || h <= 0 || w > 1<<15 || h > 1<<15 {
		return nil, newError(ReasonGenerationFailed, "rimg: bad dimensions %dx%d", w, h)
	}
	cw, ch := (w+1)/2, (h+1)/2
	want := w*h + 2*cw*ch
	fr := flate.NewReader(bytes.NewReader(data[14:]))
	defer fr.Close()
	raw, err := io.ReadAll(io.LimitReader(fr, int64(want)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) != want {
		return nil, newError(ReasonGenerationFailed, "rimg: truncated payload")
	}
	yPlane := raw[:w*h]
	cbPlane := raw[w*h : w*h+cw*ch]
	crPlane := raw[w*h+cw*ch:]
	deltaDecode(yPlane, w)
	deltaDecode(cbPlane, cw)
	deltaDecode(crPlane, cw)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	halfY := uint8(0)
	if shiftY > 0 {
		halfY = 1 << (shiftY - 1)
	}
	halfC := uint8(0)
	if shiftC > 0 {
		halfC = 1 << (shiftC - 1)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy := (yPlane[y*w+x] << shiftY) | halfY
			cb := (cbPlane[(y/2)*cw+(x/2)] << shiftC) | halfC
			cr := (crPlane[(y/2)*cw+(x/2)] << shiftC) | halfC
			r, g, b := color.YCbCrToRGB(yy, cb, cr)
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = r, g, b, 0xff
		}
	}
	return dst, nil
}

// deltaCode replaces each row with first-byte + modular differences
// to the left neighbor, which flate compresses well on photo-like
// content.
func deltaCode(plane []byte, rowLen int) {
	for off := 0; off+rowLen <= len(plane); off += rowLen {
		row := plane[off : off+rowLen]
		for i := rowLen - 1; i > 0; i-- {
			row[i] = row[i] - row[i-1]
		}
	}
}

// deltaDecode reverses deltaCode in place.
func deltaDecode(plane []byte, rowLen int) {
	for off := 0; off+rowLen <= len(plane); off += rowLen {
		row := plane[off : off+rowLen]
		for i := 1; i < rowLen; i++ {
			row[i] = row[i] + row[i-1]
		}
	}
}

func init() { registerCodec(rimgCodec{}) }
