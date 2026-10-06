package delivery

import "image"

// resizeImage scales src to w x h using deterministic bilinear
// resampling implemented in pure Go with float64 accumulation.
// Pure Go arithmetic on identical inputs is bit-deterministic across
// runs, machines of the same architecture and goroutine counts.
func resizeImage(src image.Image, w, h int) *image.RGBA {
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rectangle{})
	}
	rgba := toRGBA(src)
	sw, sh := rgba.Rect.Dx(), rgba.Rect.Dy()
	if sw == w && sh == h {
		return rgba
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Center-aligned sampling grid.
	xScale := float64(sw) / float64(w)
	yScale := float64(sh) / float64(h)
	for dy := 0; dy < h; dy++ {
		sy := (float64(dy)+0.5)*yScale - 0.5
		y0 := int(sy)
		fy := sy - float64(y0)
		if y0 < 0 {
			y0, fy = 0, 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for dx := 0; dx < w; dx++ {
			sx := (float64(dx)+0.5)*xScale - 0.5
			x0 := int(sx)
			fx := sx - float64(x0)
			if x0 < 0 {
				x0, fx = 0, 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			i00 := rgba.PixOffset(x0, y0)
			i10 := rgba.PixOffset(x1, y0)
			i01 := rgba.PixOffset(x0, y1)
			i11 := rgba.PixOffset(x1, y1)
			di := dst.PixOffset(dx, dy)
			for c := 0; c < 4; c++ {
				p00 := float64(rgba.Pix[i00+c])
				p10 := float64(rgba.Pix[i10+c])
				p01 := float64(rgba.Pix[i01+c])
				p11 := float64(rgba.Pix[i11+c])
				top := p00 + (p10-p00)*fx
				bot := p01 + (p11-p01)*fx
				v := top + (bot-top)*fy
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				dst.Pix[di+c] = uint8(v + 0.5)
			}
		}
	}
	return dst
}
