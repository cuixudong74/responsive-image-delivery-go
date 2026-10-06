package delivery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheSingleflight(t *testing.T) {
	c := NewCache(0)
	var calls atomic.Int64
	release := make(chan struct{})
	gen := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		<-release // 阻塞生成，确保等待者叠加
		return []byte("payload"), nil
	}
	const n = 32
	var wg sync.WaitGroup
	results := make([]Derivative, n)
	reused := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, r, err := c.GetOrGenerate(context.Background(), "k", VariantSpec{Width: 100}, gen)
			results[i], reused[i], errs[i] = d, r, err
		}(i)
	}
	// 等所有 goroutine 都进入后放开关。
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("want exactly 1 generation, got %d", calls.Load())
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("unexpected error: %v", errs[i])
		}
		if string(results[i].Data) != "payload" {
			t.Fatalf("bad payload: %q", results[i].Data)
		}
	}
}

func TestCacheFailureLeavesNothing(t *testing.T) {
	c := NewCache(0)
	boom := errors.New("boom")
	var calls atomic.Int64
	release := make(chan struct{})
	gen := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		<-release // 阻塞，确保等待者叠加到同一次生成
		return nil, boom
	}
	started := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			_, _, err := c.GetOrGenerate(context.Background(), "k", VariantSpec{}, gen)
			errs[i] = err
		}(i)
	}
	close(started)
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("want 1 generation attempt, got %d", calls.Load())
	}
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("waiter %d: want boom, got %v", i, err)
		}
	}
	if entries, _ := c.Stats(); entries != 0 {
		t.Fatalf("failed generation must not be cached, entries=%d", entries)
	}
	// 失败后重试应重新生成并成功。
	d, reused, err := c.GetOrGenerate(context.Background(), "k", VariantSpec{}, func(ctx context.Context) ([]byte, error) {
		return []byte("ok"), nil
	})
	if err != nil || reused || string(d.Data) != "ok" {
		t.Fatalf("retry: d=%q reused=%v err=%v", d.Data, reused, err)
	}
}

func TestCacheWaiterCancelDoesNotAffectGeneration(t *testing.T) {
	c := NewCache(0)
	release := make(chan struct{})
	gen := func(ctx context.Context) ([]byte, error) {
		<-release
		return []byte("slow"), nil
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrGenerate(context.Background(), "k", VariantSpec{}, gen)
		leaderDone <- err
	}()
	time.Sleep(20 * time.Millisecond) // 确保 leader 已进入生成
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := c.GetOrGenerate(ctx, "k", VariantSpec{}, gen)
		waiterDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter: want context.Canceled, got %v", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader must not be affected by waiter cancel: %v", err)
	}
	// 结果应对后续请求可见。
	d, reused, err := c.GetOrGenerate(context.Background(), "k", VariantSpec{}, gen)
	if err != nil || !reused || string(d.Data) != "slow" {
		t.Fatalf("after cancel: d=%q reused=%v err=%v", d.Data, reused, err)
	}
}

func TestCacheInvalidatePrefix(t *testing.T) {
	c := NewCache(0)
	put := func(key string) {
		if _, _, err := c.GetOrGenerate(context.Background(), key, VariantSpec{}, func(ctx context.Context) ([]byte, error) {
			return []byte(key), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("cfgA/hash1/w100")
	put("cfgA/hash1/w200")
	put("cfgA/hash2/w100")
	if n := c.InvalidatePrefix("cfgA/hash1/"); n != 2 {
		t.Fatalf("want 2 invalidated, got %d", n)
	}
	if entries, _ := c.Stats(); entries != 1 {
		t.Fatalf("want 1 entry left, got %d", entries)
	}
}

func TestCacheLRUBound(t *testing.T) {
	c := NewCache(250) // 最多容纳两个 100 字节条目
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("k%d", i)
		_, _, err := c.GetOrGenerate(context.Background(), key, VariantSpec{}, func(ctx context.Context) ([]byte, error) {
			return make([]byte, 100), nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, used := c.Stats()
	if used > 250 {
		t.Fatalf("cache exceeds bound: entries=%d used=%d", entries, used)
	}
	// 最早的条目应已被逐出。
	if _, reused, _ := c.GetOrGenerate(context.Background(), "k0", VariantSpec{}, func(ctx context.Context) ([]byte, error) {
		return make([]byte, 100), nil
	}); reused {
		t.Fatalf("oldest entry should have been evicted")
	}
}
