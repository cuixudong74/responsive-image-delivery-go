package delivery

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A corrupt source must fail with a distinguishable reason and must
// not poison the cache: fixing the source lets later requests succeed.
func TestCorruptSourceFailsCleanly(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("broken", []byte("this is not an image"))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 100, DevicePixelRatio: 1}
	_, err := svc.Deliver(context.Background(), Request{SourceID: "broken", Caps: caps})
	if err == nil {
		t.Fatal("want error for corrupt source")
	}
	if e, ok := err.(*Error); !ok || e.Reason != ReasonGenerationFailed {
		t.Fatalf("want ReasonGenerationFailed, got %v", err)
	}
	// Fix the source; the same request must now succeed.
	store.Put("broken", encodePNG(t, sampleImage(200, 150)))
	if _, err := svc.Deliver(context.Background(), Request{SourceID: "broken", Caps: caps}); err != nil {
		t.Fatalf("after fix: %v", err)
	}
}

// Missing source must be distinguishable from generation failures.
func TestMissingSource(t *testing.T) {
	svc, _ := newTestService(t, nil)
	_, err := svc.Deliver(context.Background(), Request{
		SourceID: "nope",
		Caps:     ClientCapabilities{AcceptFormats: allFormats},
	})
	if e, ok := err.(*Error); !ok || e.Reason != ReasonSourceNotFound {
		t.Fatalf("want ReasonSourceNotFound, got %v", err)
	}
}

// Canceling while waiting for a generation slot must return a
// canceled reason and leave nothing cached.
func TestCancelWhileQueued(t *testing.T) {
	svc, store := newTestService(t, func(c *Config) {
		c.MaxConcurrentGenerations = 1
		c.MaxQueue = 16
	})
	release := make(chan struct{})
	svc.genHook = func(ctx context.Context) {
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	store.Put("photo", encodePNG(t, sampleImage(400, 300)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 100, DevicePixelRatio: 1}

	// Occupy the single generation slot.
	first := make(chan error, 1)
	go func() {
		_, err := svc.Deliver(context.Background(), Request{SourceID: "photo", Caps: caps})
		first <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the first request reach the hook

	// This request queues for the slot; cancel it.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// Different spec so it does not coalesce with the first.
		c2 := caps
		c2.ScreenWidth = 200
		_, err := svc.Deliver(ctx, Request{SourceID: "photo", Caps: c2})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if e, ok := err.(*Error); !ok || e.Reason != ReasonCanceled {
			t.Fatalf("want ReasonCanceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not return")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first request: %v", err)
	}
	// The canceled spec must not be cached: next request regenerates.
	resp := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 200, DevicePixelRatio: 1,
	})
	if resp.Reused {
		t.Fatal("canceled generation left a cached result")
	}
}

// A generation that exceeds its timeout must fail with ReasonTimeout
// and must not cache a partial result.
func TestGenerationTimeout(t *testing.T) {
	svc, store := newTestService(t, func(c *Config) {
		c.GenerationTimeout = 50 * time.Millisecond
	})
	svc.genHook = func(ctx context.Context) {
		<-ctx.Done() // simulate a generator stuck until deadline
	}
	store.Put("photo", encodePNG(t, sampleImage(400, 300)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 100, DevicePixelRatio: 1}
	_, err := svc.Deliver(context.Background(), Request{SourceID: "photo", Caps: caps})
	if e, ok := err.(*Error); !ok || e.Reason != ReasonTimeout {
		t.Fatalf("want ReasonTimeout, got %v", err)
	}
	// Nothing cached: removing the hook lets the next request succeed.
	svc.genHook = nil
	svc.cfg.GenerationTimeout = 0
	resp := deliver(t, svc, "photo", caps)
	if resp.Reused {
		t.Fatal("timed-out generation left a cached result")
	}
}

// A failed generation must propagate the error to all coalesced
// waiters and cache nothing.
func TestFailurePropagatesToCoalescedWaiters(t *testing.T) {
	svc, store := newTestService(t, nil)
	svc.genHook = func(ctx context.Context) {
		time.Sleep(30 * time.Millisecond)
	}
	store.Put("photo", encodePNG(t, sampleImage(400, 300)))
	// Poison the codec by requesting an unregistered format? Instead
	// use a source that decodes but fails encode: not possible with
	// our codecs. Simulate failure via a hook that cancels the leader.
	// Simpler: corrupt source decoded OK at first is not possible
	// either. Use timeout as the failure trigger shared by waiters.
	svc.cfg.GenerationTimeout = 10 * time.Millisecond
	svc.genHook = func(ctx context.Context) { <-ctx.Done() }

	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 100, DevicePixelRatio: 1}
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Deliver(context.Background(), Request{SourceID: "photo", Caps: caps})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if e, ok := err.(*Error); !ok || e.Reason != ReasonTimeout {
			t.Fatalf("waiter %d: want ReasonTimeout, got %v", i, err)
		}
	}
	snap := svc.Stats().Snapshot()
	if snap.Generated != 0 {
		t.Fatalf("failed generations counted as generated: %+v", snap)
	}
}

// When the queue is full, excess requests are rejected with a
// distinguishable reason while queued requests still complete.
func TestQueueFullRejects(t *testing.T) {
	svc, store := newTestService(t, func(c *Config) {
		c.MaxConcurrentGenerations = 1
		c.MaxQueue = 2
		c.GenerationTimeout = 5 * time.Second
	})
	release := make(chan struct{})
	svc.genHook = func(ctx context.Context) {
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	// Large source so each requested width maps to a distinct ladder
	// entry (160/320/640/1280) and nothing coalesces.
	store.Put("photo", encodePNG(t, sampleImage(2000, 1500)))

	// Fill the slot (1) and the queue (2) with distinct specs.
	var wg sync.WaitGroup
	for _, w := range []int{100, 200, 500} {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			svc.Deliver(context.Background(), Request{
				SourceID: "photo",
				Caps: ClientCapabilities{
					AcceptFormats: allFormats, ScreenWidth: w, DevicePixelRatio: 1,
				},
			})
		}(w)
	}
	time.Sleep(100 * time.Millisecond) // let them queue up

	_, err := svc.Deliver(context.Background(), Request{
		SourceID: "photo",
		Caps:     ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 999, DevicePixelRatio: 1},
	})
	if e, ok := err.(*Error); !ok || e.Reason != ReasonQueueFull {
		t.Fatalf("want ReasonQueueFull, got %v", err)
	}
	snap := svc.Stats().Snapshot()
	if snap.Rejected == 0 {
		t.Fatal("rejection not counted")
	}
	close(release)
	wg.Wait()
}

// Under load with cancellation the service must not deadlock and
// memory must stay bounded (cache evicts to its byte budget).
func TestNoDeadlockUnderLoadAndCancel(t *testing.T) {
	svc, store := newTestService(t, func(c *Config) {
		c.MaxConcurrentGenerations = 2
		c.MaxQueue = 4
		c.CacheMaxBytes = 1 << 20
	})
	store.Put("photo", encodePNG(t, sampleImage(600, 400)))
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			if i%3 == 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
				defer cancel()
			}
			svc.Deliver(ctx, Request{
				SourceID: "photo",
				Caps: ClientCapabilities{
					AcceptFormats: allFormats, ScreenWidth: 50 + i*10, DevicePixelRatio: 1,
				},
			})
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock under load")
	}
	if svc.cache.size > svc.cache.maxSize {
		t.Fatalf("cache size %d exceeds budget %d", svc.cache.size, svc.cache.maxSize)
	}
}
