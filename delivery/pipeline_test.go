package delivery

import (
	"bytes"
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeEndToEnd(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1600, 900, 1))
	p, _, _ := newTestPipeline(t, "hero", srcData)

	res, err := p.Serve(context.Background(), "hero", ClientCapabilities{
		ScreenWidthCSS: 640, DPR: 1, Network: Network4G,
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if res.Selection.Spec.Format != FormatJPEG {
		t.Fatalf("want jpeg for accept-all client, got %s", res.Selection.Spec.Format)
	}
	if res.Selection.Spec.Width != 640 {
		t.Fatalf("want width 640, got %d", res.Selection.Spec.Width)
	}
	if res.Reused {
		t.Fatalf("first request must generate, not reuse")
	}
	// 返回的数据必须可被对应格式解码，且宽度正确。
	img, _, err := DecodeImage(res.Data)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if img.Bounds().Dx() != 640 {
		t.Fatalf("decoded width = %d, want 640", img.Bounds().Dx())
	}
	// 第二次相同请求必须复用。
	res2, err := p.Serve(context.Background(), "hero", ClientCapabilities{
		ScreenWidthCSS: 640, DPR: 1, Network: Network4G,
	})
	if err != nil {
		t.Fatalf("serve 2: %v", err)
	}
	if !res2.Reused {
		t.Fatalf("second request must reuse cached result")
	}
	if !bytes.Equal(res.Data, res2.Data) {
		t.Fatalf("reused result differs")
	}
	snap := p.Stats().Snapshot()
	if snap.Requests != 2 || snap.Generations != 1 || snap.CacheHits != 1 {
		t.Fatalf("bad stats: %+v", snap)
	}
}

func TestServeFallbackClientNeverGetsUndecodable(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1200, 800, 2))
	p, _, _ := newTestPipeline(t, "img", srcData)
	// 只接受 PNG 的客户端。
	res, err := p.Serve(context.Background(), "img", ClientCapabilities{
		AcceptFormats:  []string{"image/png"},
		ScreenWidthCSS: 400, DPR: 1, Network: Network4G,
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if res.Selection.Spec.Format != FormatPNG {
		t.Fatalf("want png, got %s", res.Selection.Spec.Format)
	}
	if res.Selection.Reason != ReasonFallbackFormat {
		t.Fatalf("want fallback reason, got %s", res.Selection.Reason)
	}
	if _, f, err := DecodeImage(res.Data); err != nil || f != FormatPNG {
		t.Fatalf("result must be decodable png: f=%s err=%v", f, err)
	}
	// 只接受 webp 的客户端必须得到错误而非数据。
	_, err = p.Serve(context.Background(), "img", ClientCapabilities{
		AcceptFormats: []string{"image/webp"},
	})
	if !errors.Is(err, ErrNoCommonFormat) {
		t.Fatalf("want ErrNoCommonFormat, got %v", err)
	}
}

func TestDeterministicAcrossRestartAndParallelism(t *testing.T) {
	srcData := encodeJPEG(t, makeTestImage(1024, 768, 7), 90)
	caps := ClientCapabilities{ScreenWidthCSS: 500, DPR: 2, Network: Network4G}

	serve := func() []byte {
		store := NewSourceStore()
		if _, err := store.Put("s", srcData); err != nil {
			t.Fatal(err)
		}
		p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig()})
		res, err := p.Serve(context.Background(), "s", caps)
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		return res.Data
	}
	base := serve()
	// 模拟重启：全新管线多次生成。
	for i := 0; i < 3; i++ {
		if got := serve(); !bytes.Equal(base, got) {
			t.Fatalf("run %d: output differs across restart", i)
		}
	}
	// 高并行度下同一管线并发生成。
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig(), MaxConcurrent: 8})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := p.Serve(context.Background(), "s", caps)
			if err != nil {
				t.Errorf("serve: %v", err)
				return
			}
			if !bytes.Equal(base, res.Data) {
				t.Errorf("concurrent output differs")
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentDedup(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1600, 1200, 3))
	p, _, _ := newTestPipeline(t, "big", srcData)
	caps := ClientCapabilities{ScreenWidthCSS: 800, DPR: 1, Network: Network4G}

	const n = 32
	var wg sync.WaitGroup
	var first []byte
	errs := make([]error, n)
	results := make([]Result, n)
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := p.Serve(context.Background(), "big", caps)
			results[i], errs[i] = res, err
			mu.Lock()
			if first == nil {
				first = res.Data
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if !bytes.Equal(results[i].Data, first) {
			t.Fatalf("request %d got different bytes", i)
		}
	}
	snap := p.Stats().Snapshot()
	if snap.Generations != 1 {
		t.Fatalf("want exactly 1 generation under concurrency, got %d", snap.Generations)
	}
	if snap.CacheHits != n-1 {
		t.Fatalf("want %d cache hits, got %d", n-1, snap.CacheHits)
	}
}

func TestSourceUpdateInvalidates(t *testing.T) {
	store := NewSourceStore()
	v1 := encodePNG(t, makeTestImage(1200, 900, 10))
	src1, err := store.Put("pic", v1)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig()})
	caps := ClientCapabilities{ScreenWidthCSS: 480, DPR: 1, Network: Network4G}

	res1, err := p.Serve(context.Background(), "pic", caps)
	if err != nil {
		t.Fatalf("serve v1: %v", err)
	}
	if res1.Reused {
		t.Fatalf("first serve must generate")
	}

	// 源图更新：内容不同 -> 哈希不同 -> 缓存键不同。
	v2 := encodePNG(t, makeTestImage(1200, 900, 99))
	src2, err := store.Put("pic", v2)
	if err != nil {
		t.Fatal(err)
	}
	if src1.Hash == src2.Hash {
		t.Fatalf("hash must change on content update")
	}
	p.InvalidateSource(src1) // 回收旧派生
	if entries, _ := p.CacheStats(); entries != 0 {
		t.Fatalf("old derivatives must be invalidated, entries=%d", entries)
	}

	res2, err := p.Serve(context.Background(), "pic", caps)
	if err != nil {
		t.Fatalf("serve v2: %v", err)
	}
	if res2.Reused {
		t.Fatalf("after update must regenerate, not reuse")
	}
	if bytes.Equal(res1.Data, res2.Data) {
		t.Fatalf("updated source must yield different derivative")
	}
	// 与基于新输入直接生成的结果一致。
	fresh := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig()})
	res3, err := fresh.Serve(context.Background(), "pic", caps)
	if err != nil {
		t.Fatalf("serve fresh: %v", err)
	}
	if !bytes.Equal(res2.Data, res3.Data) {
		t.Fatalf("regenerated result must equal fresh generation from new input")
	}
}

func TestConcurrentReadDuringSourceUpdate(t *testing.T) {
	store := NewSourceStore()
	v1 := encodePNG(t, makeTestImage(800, 600, 1))
	v2 := encodePNG(t, makeTestImage(800, 600, 2))
	if _, err := store.Put("pic", v1); err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig()})
	caps := ClientCapabilities{ScreenWidthCSS: 320, DPR: 1, Network: Network4G}

	// 预先算出两个版本各自的正确结果。
	serve := func() []byte {
		res, err := p.Serve(context.Background(), "pic", caps)
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		return res.Data
	}
	wantV1 := serve()
	if _, err := store.Put("pic", v2); err != nil {
		t.Fatal(err)
	}
	wantV2 := serve()

	// 并发读写：每次结果必须完整匹配某一版本，不得出现混合。
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := p.Serve(context.Background(), "pic", caps)
				if err != nil {
					t.Errorf("serve: %v", err)
					return
				}
				if !bytes.Equal(res.Data, wantV1) && !bytes.Equal(res.Data, wantV2) {
					t.Errorf("mixed/corrupt result during update")
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if _, err := store.Put("pic", v1); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put("pic", v2); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// slowCodec 包装一个 Codec，Encode 前等待闸门，用于测试慢生成与取消。
type slowCodec struct {
	inner Codec
	gate  chan struct{}
}

func (s slowCodec) Format() Format      { return s.inner.Format() }
func (s slowCodec) MediaType() string   { return s.inner.MediaType() }
func (s slowCodec) EfficiencyRank() int { return s.inner.EfficiencyRank() }
func (s slowCodec) Decode(d []byte) (image.Image, error) {
	return s.inner.Decode(d)
}
func (s slowCodec) Encode(img image.Image, q int) ([]byte, error) {
	<-s.gate
	return s.inner.Encode(img, q)
}

func TestSlowGenerationDoesNotBlockOtherSpecs(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1024, 768, 5))
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	reg := NewRegistry()
	reg.Register(slowCodec{inner: jpegCodec{}, gate: gate}) // JPEG 变慢
	reg.Register(pngCodec{})                                // PNG 保持快速
	p := NewPipeline(store, reg, Options{Config: DefaultConfig(), MaxConcurrent: 4})

	slowDone := make(chan error, 1)
	go func() {
		_, err := p.Serve(context.Background(), "s", ClientCapabilities{
			ScreenWidthCSS: 320, DPR: 1, Network: Network4G,
		})
		slowDone <- err
	}()
	time.Sleep(50 * time.Millisecond) // 确保慢生成已开始

	// 另一个规格（PNG 客户端）应能立即完成。
	fastDone := make(chan error, 1)
	go func() {
		_, err := p.Serve(context.Background(), "s", ClientCapabilities{
			AcceptFormats:  []string{"image/png"},
			ScreenWidthCSS: 320, DPR: 1, Network: Network4G,
		})
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast request: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fast request blocked by slow generation of another spec")
	}
	close(gate)
	if err := <-slowDone; err != nil {
		t.Fatalf("slow request: %v", err)
	}
}

// failCodec 的 Encode 永远失败。
type failCodec struct{ inner Codec }

func (f failCodec) Format() Format      { return f.inner.Format() }
func (f failCodec) MediaType() string   { return f.inner.MediaType() }
func (f failCodec) EfficiencyRank() int { return f.inner.EfficiencyRank() }
func (f failCodec) Decode(d []byte) (image.Image, error) {
	return f.inner.Decode(d)
}
func (f failCodec) Encode(image.Image, int) ([]byte, error) { return nil, errors.New("encode boom") }

func TestGenerationFailureNotCached(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(640, 480, 6))
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Register(failCodec{inner: jpegCodec{}})
	reg.Register(pngCodec{})
	p := NewPipeline(store, reg, Options{Config: DefaultConfig()})
	caps := ClientCapabilities{ScreenWidthCSS: 320, DPR: 1, Network: Network4G}

	if _, err := p.Serve(context.Background(), "s", caps); err == nil {
		t.Fatalf("want encode failure")
	}
	if entries, _ := p.CacheStats(); entries != 0 {
		t.Fatalf("failed generation must not be cached, entries=%d", entries)
	}
	snap := p.Stats().Snapshot()
	if snap.GenFailures != 1 {
		t.Fatalf("want 1 gen failure, got %d", snap.GenFailures)
	}
	// 修复编解码器后重试必须成功。
	reg2 := NewRegistry()
	reg2.Register(jpegCodec{})
	reg2.Register(pngCodec{})
	p2 := NewPipeline(store, reg2, Options{Config: DefaultConfig()})
	if _, err := p2.Serve(context.Background(), "s", caps); err != nil {
		t.Fatalf("retry after fix: %v", err)
	}
}

func TestCancelDuringGenerationLeavesNoPartial(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1024, 768, 8))
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	reg := NewRegistry()
	reg.Register(slowCodec{inner: jpegCodec{}, gate: gate})
	reg.Register(pngCodec{})
	p := NewPipeline(store, reg, Options{Config: DefaultConfig()})
	caps := ClientCapabilities{ScreenWidthCSS: 320, DPR: 1, Network: Network4G}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Serve(ctx, "s", caps)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // 生成已阻塞在 gate
	cancel()
	close(gate)
	if err := <-done; err == nil {
		t.Fatalf("want cancellation error")
	}
	if entries, _ := p.CacheStats(); entries != 0 {
		t.Fatalf("cancelled generation must not leave partial result, entries=%d", entries)
	}
	// 后续请求必须能正常生成有效结果。
	reg2 := NewRegistry()
	reg2.Register(jpegCodec{})
	reg2.Register(pngCodec{})
	p2 := NewPipeline(store, reg2, Options{Config: DefaultConfig()})
	res, err := p2.Serve(context.Background(), "s", caps)
	if err != nil {
		t.Fatalf("serve after cancel: %v", err)
	}
	if _, _, err := DecodeImage(res.Data); err != nil {
		t.Fatalf("result after cancel must be valid: %v", err)
	}
}

func TestBackpressureRejects(t *testing.T) {
	srcData := encodePNG(t, makeTestImage(1024, 768, 9))
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	reg := NewRegistry()
	reg.Register(slowCodec{inner: jpegCodec{}, gate: gate})
	reg.Register(pngCodec{})
	// 并发 1、队列 1：第三个不同规格的请求必须被拒绝。
	p := NewPipeline(store, reg, Options{Config: DefaultConfig(), MaxConcurrent: 1, MaxQueue: 1})

	capsFor := func(width int) ClientCapabilities {
		return ClientCapabilities{ScreenWidthCSS: width, DPR: 1, Network: Network4G}
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, w := range []int{320, 480, 640} {
		wg.Add(1)
		go func(i, w int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 30 * time.Millisecond) // 错开到达顺序
			_, errs[i] = p.Serve(context.Background(), "s", capsFor(w))
		}(i, w)
	}
	// 等待出现拒绝（容量已满），再放行被阻塞的生成。
	deadline := time.Now().Add(5 * time.Second)
	for p.Stats().Snapshot().Rejected == 0 {
		if time.Now().After(deadline) {
			close(gate)
			t.Fatalf("no rejection observed under saturation")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	wg.Wait()
	var rejected int
	for _, err := range errs {
		if errors.Is(err, ErrTooManyInFlight) {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatalf("want at least one rejection under saturation, errs=%v", errs)
	}
	if snap := p.Stats().Snapshot(); snap.Rejected == 0 {
		t.Fatalf("rejection must be counted: %+v", snap)
	}
	// 放行后系统必须恢复正常（无死锁）。
	reg2 := NewRegistry()
	reg2.Register(jpegCodec{})
	reg2.Register(pngCodec{})
	p2 := NewPipeline(store, reg2, Options{Config: DefaultConfig()})
	if _, err := p2.Serve(context.Background(), "s", capsFor(320)); err != nil {
		t.Fatalf("pipeline must recover after saturation: %v", err)
	}
}

func TestOverBudgetStats(t *testing.T) {
	srcData := encodeJPEG(t, makeTestImage(1920, 1080, 4), 90)
	store := NewSourceStore()
	if _, err := store.Put("s", srcData); err != nil {
		t.Fatal(err)
	}
	// 极小预算：任何规格都超预算。
	budgets := map[NetworkClass]Budget{NetworkUnknown: {MaxBytes: 1}}
	p := NewPipeline(store, DefaultRegistry(), Options{Config: DefaultConfig(), Budgets: budgets})
	res, err := p.Serve(context.Background(), "s", ClientCapabilities{
		ScreenWidthCSS: 640, DPR: 1, Network: NetworkSaveData,
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if res.Selection.WithinBudget || res.Selection.Reason != ReasonOverBudgetFloor {
		t.Fatalf("want over budget floor, got %+v", res.Selection)
	}
	if snap := p.Stats().Snapshot(); snap.OverBudget != 1 {
		t.Fatalf("over-budget must be observable: %+v", snap)
	}
}

func TestSourceNotFound(t *testing.T) {
	p := NewPipeline(NewSourceStore(), DefaultRegistry(), Options{Config: DefaultConfig()})
	_, err := p.Serve(context.Background(), "missing", ClientCapabilities{})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("want ErrSourceNotFound, got %v", err)
	}
}

func TestLimiterDirect(t *testing.T) {
	l := NewLimiter(1, 1)
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 占满后：一个可排队，第二个立即拒绝。
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- l.Acquire(ctx) }()
	time.Sleep(20 * time.Millisecond)
	if err := l.Acquire(context.Background()); !errors.Is(err, ErrTooManyInFlight) {
		t.Fatalf("want ErrTooManyInFlight, got %v", err)
	}
	cancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued waiter: want canceled, got %v", err)
	}
	l.Release()
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l.Release()
}

var _ = atomic.Int64{} // 保留 atomic 引用以防未来测试需要
