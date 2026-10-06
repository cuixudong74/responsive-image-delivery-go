package delivery

import (
	"image"
	"image/color"
)

// Resize 把图片缩放到目标宽度（高度按比例，至少为 1），
// 使用确定性的双线性重采样：全部计算基于 float64 的固定顺序运算，
// 相同输入在任何并行度与进程重启下输出逐字节一致。
// targetWidth <=0 或 >= 源宽时返回原图的 NRGBA 副本（不放大）。
func Resize(img image.Image, targetWidth int) image.Image {
	src := toNRGBA(img)
	sw := src.Bounds().Dx()
	sh := src.Bounds().Dy()
	if targetWidth <= 0 || targetWidth >= sw {
		return src
	}
	dw := targetWidth
	dh := sh * targetWidth / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	// 目标像素中心映射回源坐标。
	scaleX := float64(sw) / float64(dw)
	scaleY := float64(sh) / float64(dh)
	for y := 0; y < dh; y++ {
		sy := (float64(y)+0.5)*scaleY - 0.5
		y0, fy := floorFrac(sy)
		y1 := y0 + 1
		if y0 < 0 {
			y0, y1 = 0, 0
		}
		if y1 > sh-1 {
			y1 = sh - 1
			if y0 > sh-1 {
				y0 = sh - 1
			}
		}
		for x := 0; x < dw; x++ {
			sx := (float64(x)+0.5)*scaleX - 0.5
			x0, fx := floorFrac(sx)
			x1 := x0 + 1
			if x0 < 0 {
				x0, x1 = 0, 0
			}
			if x1 > sw-1 {
				x1 = sw - 1
				if x0 > sw-1 {
					x0 = sw - 1
				}
			}
			dst.SetNRGBA(x, y, bilinear(src, x0, y0, x1, y1, fx, fy))
		}
	}
	return dst
}

// floorFrac 返回 v 的向下取整与小数部分。
func floorFrac(v float64) (int, float64) {
	i := int(v)
	if v < 0 && float64(i) != v {
		i--
	}
	return i, v - float64(i)
}

// bilinear 在 (x0,y0)-(x1,y1) 之间按 (fx,fy) 双线性插值。
func bilinear(img *image.NRGBA, x0, y0, x1, y1 int, fx, fy float64) color.NRGBA {
	c00 := nrgbaAt(img, x0, y0)
	c10 := nrgbaAt(img, x1, y0)
	c01 := nrgbaAt(img, x0, y1)
	c11 := nrgbaAt(img, x1, y1)
	w00 := (1 - fx) * (1 - fy)
	w10 := fx * (1 - fy)
	w01 := (1 - fx) * fy
	w11 := fx * fy
	mix := func(a, b, c, d uint8) uint8 {
		v := float64(a)*w00 + float64(b)*w10 + float64(c)*w01 + float64(d)*w11
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v + 0.5)
	}
	return color.NRGBA{
		R: mix(c00.R, c10.R, c01.R, c11.R),
		G: mix(c00.G, c10.G, c01.G, c11.G),
		B: mix(c00.B, c10.B, c01.B, c11.B),
		A: mix(c00.A, c10.A, c01.A, c11.A),
	}
}

func nrgbaAt(img *image.NRGBA, x, y int) color.NRGBA {
	i := img.PixOffset(x, y)
	return color.NRGBA{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}
}

// toNRGBA 把任意图片转换为 NRGBA，便于逐字节确定的重采样。
func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	if nr, ok := src.(*image.NRGBA); ok && nr.Bounds().Eq(b) && b.Min == (image.Point{}) {
		return nr
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
