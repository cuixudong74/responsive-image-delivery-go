package delivery

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent identical requests must trigger exactly one generation;
// all others wait and share the result.
func TestConcurrentRequestsCoalesce(t *testing.T) {
	svc, store := newTestService(t, nil)
	var gens atomic.Int64
	svc.genHook = func(context.Context) {
		gens.Add(1)
		time.Sleep(20 * time.Millisecond) // make overlap likely
	}
	store.Put("photo", encodePNG(t, sampleImage(800, 600)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g"}

	const n = 32
	var wg sync.WaitGroup
	results := make([]*Response, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Deliver(context.Background(), Request{SourceID: "photo", Caps: caps})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if g := gens.Load(); g != 1 {
		t.Fatalf("expected exactly 1 generation, got %d", g)
	}
	for i := 1; i < n; i++ {
		if string(results[i].Data) != string(results[0].Data) {
			t.Fatalf("request %d got different bytes", i)
		}
	}
	snap := svc.Stats().Snapshot()
	if snap.Generated != 1 {
		t.Fatalf("stats: generated=%d, want 1", snap.Generated)
	}
	if snap.Coalesced+snap.CacheHits != n-1 {
		t.Fatalf("stats: coalesced=%d hits=%d, want %d total reuse", snap.Coalesced, snap.CacheHits, n-1)
	}
}

// A slow generation of one spec must not block different specs.
func TestSlowGenerationDoesNotBlockOtherSpecs(t *testing.T) {
	svc, store := newTestService(t, nil)
	slow := make(chan struct{})
	var started atomic.Int64
	svc.genHook = func(context.Context) {
		if started.Add(1) == 1 {
			<-slow // first generation blocks
		}
	}
	store.Put("photo", encodePNG(t, sampleImage(800, 600)))

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		svc.Deliver(context.Background(), Request{
			SourceID: "photo",
			Caps:     ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 100, DevicePixelRatio: 1, EffectiveType: "4g"},
		})
	}()
	// Wait until the slow generation is in flight.
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// A different spec must complete despite the blocked first one.
	done := make(chan error, 1)
	go func() {
		_, err := svc.Deliver(context.Background(), Request{
			SourceID: "photo",
			Caps:     ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 500, DevicePixelRatio: 1, EffectiveType: "4g"},
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second spec: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second spec blocked by slow generation of first spec")
	}
	close(slow)
	<-firstDone
}

// Repeated requests after generation must be cache hits.
func TestCacheHitAfterGeneration(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(800, 600)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g"}
	r1 := deliver(t, svc, "photo", caps)
	if r1.Reused {
		t.Fatal("first request should generate")
	}
	r2 := deliver(t, svc, "photo", caps)
	if !r2.Reused || r2.Coalesced {
		t.Fatal("second request should be a cache hit")
	}
	snap := svc.Stats().Snapshot()
	if snap.Generated != 1 || snap.CacheHits != 1 {
		t.Fatalf("snapshot %+v", snap)
	}
}
