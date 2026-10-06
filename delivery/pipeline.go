package delivery

import (
	"context"
	"errors"
	"fmt"
)

// ErrSourceNotFound 表示请求的源图不存在。
var ErrSourceNotFound = errors.New("delivery: source not found")

// Result 是一次交付请求的最终结果。
type Result struct {
	Data       []byte
	Selection  Selection
	SourceHash string
	Reused     bool // true 表示复用了已有/并发中的结果，未触发新生成
	CacheKey   string
	Bytes      int64 // 实际交付字节数
}

// Pipeline 串联协商、缓存去重、限流与确定性生成。
type Pipeline struct {
	sources *SourceStore
	neg     *Negotiator
	reg     *Registry
	cfg     Config
	cache   *Cache
	limiter *Limiter
	stats   *Stats
}

// Options 是 Pipeline 的构造参数。
type Options struct {
	Config        Config
	Budgets       map[NetworkClass]Budget
	CacheMaxBytes int64
	MaxConcurrent int // 最大并发生成数，<=0 默认 4
	MaxQueue      int // 最大排队数，<=0 默认 64
}

// NewPipeline 构造交付管线。
func NewPipeline(sources *SourceStore, reg *Registry, opts Options) *Pipeline {
	if reg == nil {
		reg = DefaultRegistry()
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.MaxQueue <= 0 {
		opts.MaxQueue = 64
	}
	return &Pipeline{
		sources: sources,
		neg:     NewNegotiator(reg, opts.Config, opts.Budgets),
		reg:     reg,
		cfg:     opts.Config,
		cache:   NewCache(opts.CacheMaxBytes),
		limiter: NewLimiter(opts.MaxConcurrent, opts.MaxQueue),
		stats:   &Stats{},
	}
}

// Stats 返回统计句柄。
func (p *Pipeline) Stats() *Stats { return p.stats }

// CacheKey 计算 (生成配置, 源图内容, 规格) 对应的缓存键。
// 源图内容或配置变化都会得到新键，旧键自然失效。
func (p *Pipeline) CacheKey(src Source, spec VariantSpec) string {
	return fmt.Sprintf("%s/%s/%s", p.cfg.Hash(), src.Hash, spec.String())
}

// estimateBytes 是确定性的体积启发式估计：与像素数成正比，
// 按格式与质量给出每像素字节系数。仅用于预算选择，不影响交付内容。
func (p *Pipeline) estimateBytes(src Source, spec VariantSpec) int64 {
	h := int64(src.Height) * int64(spec.Width) / int64(max(src.Width, 1))
	if h < 1 {
		h = 1
	}
	pixels := int64(spec.Width) * h
	var bppMilli int64 // 每像素字节数 × 1000
	switch spec.Format {
	case FormatJPEG:
		bppMilli = 220 * int64(spec.Quality) / 80
	default: // PNG 等无损格式
		bppMilli = 1200
	}
	return pixels*bppMilli/1000 + 512 // 加上固定的容器开销估计
}

// Serve 完成一次端到端交付：协商 -> 预算选规格 -> 去重生成/复用。
func (p *Pipeline) Serve(ctx context.Context, sourceID string, caps ClientCapabilities) (Result, error) {
	p.stats.Requests.Add(1)
	src, ok := p.sources.Get(sourceID)
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrSourceNotFound, sourceID)
	}
	sel, err := p.neg.Select(src.Width, caps, func(spec VariantSpec) (int64, error) {
		return p.estimateBytes(src, spec), nil
	})
	if err != nil {
		return Result{}, err
	}
	key := p.CacheKey(src, sel.Spec)
	d, reused, err := p.cache.GetOrGenerate(ctx, key, sel.Spec, func(ctx context.Context) ([]byte, error) {
		return p.generate(ctx, src, sel.Spec)
	})
	if err != nil {
		if errors.Is(err, ErrTooManyInFlight) {
			p.stats.Rejected.Add(1)
		}
		return Result{}, err
	}
	if reused {
		p.stats.CacheHits.Add(1)
	}
	if !sel.WithinBudget {
		p.stats.OverBudget.Add(1)
	}
	return Result{
		Data:       d.Data,
		Selection:  sel,
		SourceHash: src.Hash,
		Reused:     reused,
		CacheKey:   key,
		Bytes:      d.Bytes,
	}, nil
}

// generate 在限流保护下确定性地生成派生：解码 -> 缩放 -> 编码。
// 输出只取决于源图内容、规格与配置；失败/取消时不产生任何可见结果。
func (p *Pipeline) generate(ctx context.Context, src Source, spec VariantSpec) ([]byte, error) {
	if err := p.limiter.Acquire(ctx); err != nil {
		return nil, err
	}
	defer p.limiter.Release()
	p.stats.Generations.Add(1)

	codec, ok := p.reg.Get(spec.Format)
	if !ok {
		p.stats.GenFailures.Add(1)
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, spec.Format)
	}
	img, err := src.Decode()
	if err != nil {
		p.stats.GenFailures.Add(1)
		return nil, err
	}
	resized := Resize(img, spec.Width)
	data, err := codec.Encode(resized, spec.Quality)
	if err != nil {
		p.stats.GenFailures.Add(1)
		return nil, err
	}
	// 生成期间请求被取消/超时：丢弃完整结果，不提交缓存，
	// 保证取消不会留下会被后续请求当作有效结果的产物。
	if err := ctx.Err(); err != nil {
		p.stats.GenFailures.Add(1)
		return nil, err
	}
	return data, nil
}

// InvalidateSource 在源图更新后失效其全部派生缓存，返回失效条目数。
// 由于缓存键包含内容哈希，旧键本就不会再被新请求命中；
// 此操作主要用于及时回收内存。
func (p *Pipeline) InvalidateSource(src Source) int {
	n := p.cache.InvalidatePrefix(p.cfg.Hash() + "/" + src.Hash + "/")
	p.stats.Invalidations.Add(int64(n))
	return n
}

// InvalidateConfig 在配置变化后失效旧配置下的全部派生（旧配置哈希前缀）。
func (p *Pipeline) InvalidateConfig(oldCfg Config) int {
	n := p.cache.InvalidatePrefix(oldCfg.Hash() + "/")
	p.stats.Invalidations.Add(int64(n))
	return n
}

// CacheStats 返回缓存条目数与占用字节数（用于观测与测试）。
func (p *Pipeline) CacheStats() (entries int, usedBytes int64) { return p.cache.Stats() }
