package delivery

import (
	"bytes"
	"context"
	"image"
	"sync"
	"testing"
)

// After a source update, Deliver must return derivatives of the new
// content, identical to what a fresh service would produce from the
// new source directly.
func TestSourceUpdateInvalidates(t *testing.T) {
	svc, store := newTestService(t, nil)
	old := encodePNG(t, sampleImage(800, 600))
	store.Put("photo", old)
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g"}
	r1 := deliver(t, svc, "photo", caps)

	// Update the source with different content.
	newImg := sampleImage(800, 600)
	for i := range newImg.Pix { // make it visibly different
		newImg.Pix[i] = 255 - newImg.Pix[i]
	}
	newPNG := encodePNG(t, newImg)
	store.Put("photo", newPNG)
	svc.Invalidate("photo")

	r2 := deliver(t, svc, "photo", caps)
	if r2.SourceHash == r1.SourceHash {
		t.Fatal("hash did not change after content update")
	}
	if bytes.Equal(r1.Data, r2.Data) {
		t.Fatal("stale derivative returned after source update")
	}
	if r2.Reused {
		t.Fatal("derivative of new content must be freshly generated")
	}

	// Must equal direct generation from the new input.
	svc2, store2 := newTestService(t, nil)
	store2.Put("photo", newPNG)
	r3 := deliver(t, svc2, "photo", caps)
	if !bytes.Equal(r2.Data, r3.Data) {
		t.Fatal("regenerated result differs from direct generation on new input")
	}
}

// Old results must never be served after invalidation, even across
// many repeated requests.
func TestOldResultNeverReturnedAfterUpdate(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(640, 480)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 300, DevicePixelRatio: 1, EffectiveType: "4g"}
	r1 := deliver(t, svc, "photo", caps)

	img2 := sampleImage(640, 480)
	for i := range img2.Pix {
		img2.Pix[i] = 255 - img2.Pix[i]
	}
	store.Put("photo", encodePNG(t, img2))
	svc.Invalidate("photo")

	for i := 0; i < 5; i++ {
		resp := deliver(t, svc, "photo", caps)
		if bytes.Equal(resp.Data, r1.Data) {
			t.Fatalf("iteration %d: stale result returned", i)
		}
		if resp.SourceHash == r1.SourceHash {
			t.Fatalf("iteration %d: stale hash", i)
		}
	}
}

// A reader that resolved the source before an update completes with a
// coherent old version; a reader after sees the new version. No
// mixed size/content results may appear.
func TestConcurrentUpdateCoherence(t *testing.T) {
	svc, store := newTestService(t, nil)
	pngA := encodePNG(t, sampleImage(640, 480))
	imgB := sampleImage(900, 500) // different dimensions AND content
	pngB := encodePNG(t, imgB)
	store.Put("photo", pngA)

	// Pre-generate both versions' derivatives to know valid outputs.
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 300, DevicePixelRatio: 1, EffectiveType: "4g"}
	refA := deliver(t, svc, "photo", caps)
	svc.Invalidate("photo")
	store.Put("photo", pngB)
	refB := deliver(t, svc, "photo", caps)
	store.Put("photo", pngA)
	svc.Invalidate("photo")

	// Hammer Deliver while flipping the source.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := svc.Deliver(context.Background(), Request{SourceID: "photo", Caps: caps})
				if err != nil {
					t.Errorf("deliver: %v", err)
					return
				}
				isA := bytes.Equal(resp.Data, refA.Data)
				isB := bytes.Equal(resp.Data, refB.Data)
				if !isA && !isB {
					t.Errorf("mixed/unknown result: hash=%s version=%d len=%d",
						resp.SourceHash, resp.SourceVersion, len(resp.Data))
					return
				}
				// Content must match the version it claims.
				if isA && resp.SourceVersion != 1 && resp.SourceHash != hashBytes(pngA) {
					t.Errorf("version/content mismatch")
					return
				}
			}
		}()
	}
	// Flip the source back and forth.
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			store.Put("photo", pngB)
		} else {
			store.Put("photo", pngA)
		}
		svc.Invalidate("photo")
	}
	close(stop)
	wg.Wait()
}

// Config change must invalidate: new config produces new cache keys.
func TestConfigChangeRecomputes(t *testing.T) {
	pngB := encodePNG(t, sampleImage(800, 600))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 1, EffectiveType: "4g"}
	svc1, store1 := newTestService(t, func(c *Config) { c.QualityDefault = 80 })
	store1.Put("photo", pngB)
	r1 := deliver(t, svc1, "photo", caps)

	svc2, store2 := newTestService(t, func(c *Config) { c.QualityDefault = 40 })
	store2.Put("photo", pngB)
	r2 := deliver(t, svc2, "photo", caps)

	if bytes.Equal(r1.Data, r2.Data) {
		t.Fatal("quality change did not affect output")
	}
}

var _ = image.NewRGBA
