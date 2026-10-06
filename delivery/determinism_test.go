package delivery

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
)

var detCaps = ClientCapabilities{
	AcceptFormats: allFormats, ScreenWidth: 360, DevicePixelRatio: 2, EffectiveType: "4g",
}

// The same source + spec + config must produce byte-identical output
// across repeated generations.
func TestDeterministicRepeatGeneration(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(900, 600)))
	first := deliver(t, svc, "photo", detCaps)
	svc.Invalidate("photo") // force regeneration
	for i := 0; i < 3; i++ {
		resp := deliver(t, svc, "photo", detCaps)
		if resp.Reused {
			t.Fatalf("iteration %d: expected fresh generation after invalidate", i)
		}
		if !bytes.Equal(first.Data, resp.Data) {
			t.Fatalf("iteration %d: output differs across generations", i)
		}
		svc.Invalidate("photo")
	}
}

// Changing parallelism must not change output bytes.
func TestDeterministicAcrossParallelism(t *testing.T) {
	pngB := encodePNG(t, sampleImage(900, 600))
	var outputs [][]byte
	for _, par := range []int{1, 8} {
		svc, store := newTestService(t, func(c *Config) { c.MaxConcurrentGenerations = par })
		store.Put("photo", pngB)
		const n = 16
		results := make([][]byte, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp, err := svc.Deliver(context.Background(), Request{
					SourceID: "photo",
					Caps: ClientCapabilities{
						AcceptFormats: allFormats, ScreenWidth: 200 + i*20,
						DevicePixelRatio: 1, EffectiveType: "4g",
					},
				})
				if err != nil {
					t.Error(err)
					return
				}
				results[i] = resp.Data
			}(i)
		}
		wg.Wait()
		outputs = append(outputs, bytes.Join(results, []byte{0}))
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("outputs differ between parallelism 1 and 8")
	}
}

// A "restart" (fresh service over the same source bytes) must
// reproduce identical derivatives.
func TestDeterministicAcrossRestart(t *testing.T) {
	pngB := encodePNG(t, sampleImage(900, 600))
	svc1, store1 := newTestService(t, nil)
	store1.Put("photo", pngB)
	r1 := deliver(t, svc1, "photo", detCaps)

	svc2, store2 := newTestService(t, nil)
	store2.Put("photo", pngB)
	r2 := deliver(t, svc2, "photo", detCaps)

	if !bytes.Equal(r1.Data, r2.Data) {
		t.Fatal("output differs across service restart")
	}
	if r1.SourceHash != r2.SourceHash {
		t.Fatal("source hash differs for identical content")
	}
}

// Each codec must be byte-deterministic on its own.
func TestCodecDeterminism(t *testing.T) {
	img := sampleImage(128, 96)
	for _, f := range []Format{FormatJPEG, FormatPNG, FormatRIMG} {
		c := codecFor(f)
		a, err := c.Encode(img, 75)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		b, err := c.Encode(img, 75)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s encode not deterministic", f)
		}
		if _, err := c.Decode(a); err != nil {
			t.Fatalf("%s decode own output: %v", f, err)
		}
	}
}

// Cache keys must separate specs: different widths never share bytes.
func TestCacheKeySeparatesSpecs(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1200, 800)))
	seen := map[string]int{}
	for _, w := range []int{100, 200, 500, 1000} { // map to distinct ladder widths
		resp := deliver(t, svc, "photo", ClientCapabilities{
			AcceptFormats: allFormats, ScreenWidth: w, DevicePixelRatio: 1, EffectiveType: "4g",
		})
		key := fmt.Sprintf("%x", resp.Data[:16])
		if prev, dup := seen[key]; dup {
			t.Fatalf("width %d and %d produced identical prefixes", w, prev)
		}
		seen[key] = w
	}
}
