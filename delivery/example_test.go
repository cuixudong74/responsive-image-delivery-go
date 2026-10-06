package delivery_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"

	"github.com/cuixudong74/responsive-image-delivery-go/delivery"
)

// End-to-end: register a source, deliver to two different clients and
// inspect the statistics.
func Example() {
	// A source image generated in process.
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(x % 256)
			img.Pix[i+1] = uint8(y % 256)
			img.Pix[i+2] = 128
			img.Pix[i+3] = 255
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)

	svc := delivery.NewService(delivery.DefaultConfig(), nil)
	svc.Sources().Put("hero", buf.Bytes())

	// Modern client on a phone: 360 CSS px @ 3x on 4g.
	resp, err := svc.Deliver(context.Background(), delivery.Request{
		SourceID: "hero",
		Caps: delivery.ClientCapabilities{
			AcceptFormats:    []delivery.Format{delivery.FormatRIMG, delivery.FormatJPEG, delivery.FormatPNG},
			ScreenWidth:      360,
			DevicePixelRatio: 3,
			EffectiveType:    "4g",
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("format=%s spec=%dx%d q=%d bytes=%d withinBudget=%v reused=%v\n",
		resp.Selection.Spec.Format, resp.Selection.Spec.Width, resp.Selection.Spec.Height,
		resp.Selection.Spec.Quality, len(resp.Data), resp.Selection.WithinBudget, resp.Reused)

	// Same request again: served from cache.
	resp2, _ := svc.Deliver(context.Background(), delivery.Request{
		SourceID: "hero",
		Caps: delivery.ClientCapabilities{
			AcceptFormats:    []delivery.Format{delivery.FormatRIMG, delivery.FormatJPEG, delivery.FormatPNG},
			ScreenWidth:      360,
			DevicePixelRatio: 3,
			EffectiveType:    "4g",
		},
	})
	fmt.Printf("reused=%v\n", resp2.Reused)

	snap := svc.Stats().Snapshot()
	fmt.Printf("requests=%d generated=%d cacheHits=%d\n", snap.Requests, snap.Generated, snap.CacheHits)
	// Output:
	// format=rimg spec=1280x720 q=80 bytes=19730 withinBudget=true reused=false
	// reused=true
	// requests=2 generated=1 cacheHits=1
}
