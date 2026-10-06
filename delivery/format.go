// Package delivery 提供响应式图片的协商交付与预算优化能力。
package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// Format 表示一种图片编码格式。
type Format string

const (
	FormatJPEG Format = "image/jpeg"
	FormatPNG  Format = "image/png"
)

// ErrUnsupportedFormat 表示请求了未注册的格式。
var ErrUnsupportedFormat = errors.New("delivery: unsupported format")

// Codec 负责把图片编码为某一格式，或从字节解码。
// 同一格式同一参数下，Encode 必须对相同输入产生逐字节一致的输出。
type Codec interface {
	// Format 返回该编解码器对应的格式标识。
	Format() Format
	// MediaType 返回用于内容协商的 MIME 类型。
	MediaType() string
	// EfficiencyRank 返回体积效率排名，数值越小表示同画质下体积通常越小。
	EfficiencyRank() int
	// Encode 以给定质量（1-100）编码图片，输出必须确定性。
	Encode(img image.Image, quality int) ([]byte, error)
	// Decode 解码字节为图片。
	Decode(data []byte) (image.Image, error)
}

// Registry 是格式到编解码器的注册表，协商时按效率排序。
type Registry struct {
	codecs map[Format]Codec
}

// NewRegistry 返回一个空注册表。
func NewRegistry() *Registry { return &Registry{codecs: map[Format]Codec{}} }

// Register 注册一个编解码器，重复注册同一格式会覆盖。
func (r *Registry) Register(c Codec) { r.codecs[c.Format()] = c }

// Get 按格式查找编解码器。
func (r *Registry) Get(f Format) (Codec, bool) { c, ok := r.codecs[f]; return c, ok }

// FormatsByEfficiency 返回按体积效率（rank 升序，同 rank 按格式名）排序的全部格式，
// 排序结果确定，保证协商结果唯一。
func (r *Registry) FormatsByEfficiency() []Codec {
	cs := make([]Codec, 0, len(r.codecs))
	for _, c := range r.codecs {
		cs = append(cs, c)
	}
	sortCodecs(cs)
	return cs
}

func sortCodecs(cs []Codec) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0; j-- {
			a, b := cs[j-1], cs[j]
			if a.EfficiencyRank() > b.EfficiencyRank() ||
				(a.EfficiencyRank() == b.EfficiencyRank() && a.Format() > b.Format()) {
				cs[j-1], cs[j] = b, a
			} else {
				break
			}
		}
	}
}

// DefaultRegistry 返回内置 JPEG 与 PNG 编解码器的注册表。
// JPEG 是体积更小的首选格式，PNG 是通用回退格式。
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(jpegCodec{})
	r.Register(pngCodec{})
	return r
}

// DecodeImage 按字节内容自动探测格式并解码（支持 jpeg/png/gif）。
func DecodeImage(data []byte) (image.Image, Format, error) {
	_, name, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("delivery: probe source format: %w", err)
	}
	var img image.Image
	switch name {
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
		return img, FormatJPEG, err
	case "png":
		img, err = png.Decode(bytes.NewReader(data))
		return img, FormatPNG, err
	case "gif":
		img, err = gif.Decode(bytes.NewReader(data))
		return img, Format("image/gif"), err
	default:
		return nil, "", fmt.Errorf("delivery: %w: %s", ErrUnsupportedFormat, name)
	}
}

// clampQuality 把质量参数收敛到 [1,100]，0 视为默认值 80。
func clampQuality(q int) int {
	if q <= 0 {
		return 80
	}
	if q > 100 {
		return 100
	}
	return q
}

type jpegCodec struct{}

func (jpegCodec) Format() Format      { return FormatJPEG }
func (jpegCodec) MediaType() string   { return string(FormatJPEG) }
func (jpegCodec) EfficiencyRank() int { return 1 }

func (jpegCodec) Encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	// jpeg.Encode 对相同像素与相同参数输出确定（无随机/时间依赖）。
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: clampQuality(quality)}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (jpegCodec) Decode(data []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(data))
}

type pngCodec struct{}

func (pngCodec) Format() Format      { return FormatPNG }
func (pngCodec) MediaType() string   { return string(FormatPNG) }
func (pngCodec) EfficiencyRank() int { return 2 }

func (pngCodec) Encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	// 固定压缩级别保证确定性；quality 对 PNG 不生效（无损）。
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (pngCodec) Decode(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}
