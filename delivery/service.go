package delivery

import (
	"context"
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
)

// Service ties negotiation, selection, deterministic generation,
// deduplicated caching, invalidation and backpressure together.
type Service struct {
	cfg      Config
	cfgID    int
	sources  *SourceStore
	cache    *cache
	stats    *Stats
	sem      chan struct{}
	mu       sync.Mutex
	waiting  int
	decoded  map[string]image.Image // decoded sources by content hash
	decodedN int
	// genHook, when set, runs inside every generation. Test hook.
	genHook func(ctx context.Context)
}

// NewService creates a Service with the given config and source store.
func NewService(cfg Config, sources *SourceStore) *Service {
	if sources == nil {
		sources = NewSourceStore()
	}
	return &Service{
		cfg:     cfg,
		cfgID:   configID(cfg),
		sources: sources,
		cache:   newCache(cfg.CacheMaxBytes),
		stats:   newStats(cfg.StatsCapacity),
		sem:     make(chan struct{}, max(1, cfg.MaxConcurrentGenerations)),
		decoded: map[string]image.Image{},
	}
}

// configID folds output-relevant configuration into the cache key so
// a config change invalidates previous derivatives.
func configID(cfg Config) int {
	var sb strings.Builder
	w := append([]int(nil), cfg.Widths...)
	sort.Ints(w)
	fmt.Fprintf(&sb, "%v|%d|%d", w, cfg.QualityDefault, cfg.QualitySaveData)
	h := 0
	for _, b := range []byte(sb.String()) {
		h = h*31 + int(b)
	}
	return h
}

// Request is one delivery request.
type Request struct {
	SourceID string
	Caps     ClientCapabilities
}

// Response is the negotiated, budget-checked derivative.
type Response struct {
	Data          []byte
	Selection     Selection
	Reused        bool // cache hit or coalesced wait
	Coalesced     bool // waited on an in-flight generation
	SourceHash    string
	SourceVersion uint64
}

// Stats exposes delivery statistics.
func (s *Service) Stats() *Stats { return s.stats }

// Sources exposes the source store.
func (s *Service) Sources() *SourceStore { return s.sources }

// Invalidate drops all cached derivatives of a source. Because cache
// keys embed the source content hash and version, updated sources are
// unreachable even without an explicit Invalidate; this method also
// reclaims the memory immediately.
func (s *Service) Invalidate(sourceID string) int {
	src := s.sources.Get(sourceID)
	if src == nil {
		return 0
	}
	s.mu.Lock()
	delete(s.decoded, src.Hash)
	s.mu.Unlock()
	return s.cache.invalidateHash(src.Hash)
}

// Deliver negotiates, selects, generates or reuses a derivative and
// checks it against the budget.
func (s *Service) Deliver(ctx context.Context, req Request) (*Response, error) {
	src := s.sources.Get(req.SourceID)
	if src == nil {
		err := newError(ReasonSourceNotFound, "source %q", req.SourceID)
		s.stats.record(Event{SourceID: req.SourceID, Kind: EventFailed, Reason: err.Reason})
		return nil, err
	}
	format, err := NegotiateFormat(req.Caps)
	if err != nil {
		s.stats.record(Event{SourceID: req.SourceID, Kind: EventRejected, Reason: ReasonNoCommonFormat})
		return nil, err
	}
	img, err := s.decodeSourceCached(src)
	if err != nil {
		s.stats.record(Event{SourceID: req.SourceID, Kind: EventFailed, Reason: ReasonGenerationFailed})
		return nil, err
	}
	budget := budgetFor(req.Caps, s.cfg)
	ladder := candidateLadder(img.Bounds().Dx(), img.Bounds().Dy(), format, req.Caps, s.cfg)
	if len(ladder) == 0 {
		err := newError(ReasonInvalidRequest, "no candidate variants for source %q", req.SourceID)
		s.stats.record(Event{SourceID: req.SourceID, Kind: EventFailed, Reason: err.Reason})
		return nil, err
	}
	for i, spec := range ladder {
		key := cacheKey{
			SrcHash: src.Hash, Version: src.Version,
			Width: spec.Width, Height: spec.Height,
			Format: spec.Format, Quality: spec.Quality,
			ConfigID: s.cfgID,
		}
		data, reused, coalesced, err := s.getOrGenerate(ctx, key, img, spec)
		if err != nil {
			s.stats.record(Event{
				SourceID: req.SourceID, Spec: spec, Kind: kindOf(err),
				Reason: reasonOf(err), Coalesced: coalesced, Reused: reused,
			})
			return nil, err
		}
		within := len(data) <= budget.MaxBytes
		if within || i == len(ladder)-1 {
			reason := ReasonWithinBudget
			if !within {
				reason = ReasonOverBudgetSmallest
			}
			sel := Selection{
				Spec: spec, Budget: budget, WithinBudget: within, Reason: reason,
				Explanation: explainSelection(req.Caps, budget, spec, len(data), within, i+1),
			}
			s.stats.record(Event{
				SourceID: req.SourceID, Spec: spec, Kind: EventServed,
				Reused: reused || coalesced, Coalesced: coalesced,
				WithinBudget: within, Reason: reason, Bytes: len(data),
			})
			return &Response{
				Data: data, Selection: sel, Reused: reused || coalesced,
				Coalesced: coalesced, SourceHash: src.Hash, SourceVersion: src.Version,
			}, nil
		}
	}
	// Unreachable: the loop always returns on the last ladder entry.
	return nil, newError(ReasonInvalidRequest, "empty selection")
}

func kindOf(err error) EventKind {
	if e, ok := err.(*Error); ok && e.Reason == ReasonQueueFull {
		return EventRejected
	}
	return EventFailed
}

func reasonOf(err error) ReasonCode {
	if e, ok := err.(*Error); ok {
		return e.Reason
	}
	return ReasonGenerationFailed
}

// getOrGenerate returns a cached derivative or generates it exactly
// once per key, coalescing concurrent requests.
func (s *Service) getOrGenerate(ctx context.Context, key cacheKey, img image.Image, spec VariantSpec) (data []byte, reused, coalesced bool, err error) {
	if d, ok := s.cache.get(key); ok {
		return d, true, false, nil
	}
	d, oc, err := s.cache.do(key, func() ([]byte, error) {
		gctx := ctx
		if s.cfg.GenerationTimeout > 0 {
			var cancel context.CancelFunc
			gctx, cancel = context.WithTimeout(ctx, s.cfg.GenerationTimeout)
			defer cancel()
		}
		return s.generate(gctx, img, spec)
	})
	if err != nil {
		return nil, false, oc == outcomeCoalesced, err
	}
	switch oc {
	case outcomeHit:
		return d, true, false, nil
	case outcomeCoalesced:
		return d, false, true, nil
	}
	return d, false, false, nil
}

// generate produces one derivative deterministically, bounded by the
// concurrency semaphore, the queue limit and the generation timeout.
// Failures and cancellations never store partial results.
func (s *Service) generate(ctx context.Context, img image.Image, spec VariantSpec) ([]byte, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	if s.genHook != nil {
		s.genHook(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err, "before encode")
	}
	resized := resizeImage(img, spec.Width, spec.Height)
	data, err := codecFor(spec.Format).Encode(resized, spec.Quality)
	if err != nil {
		return nil, newError(ReasonGenerationFailed, "encode %s: %v", spec.Format, err)
	}
	return data, nil
}

// acquire takes a generation slot, bounding the number of waiters.
func (s *Service) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.waiting >= s.cfg.MaxQueue {
		s.mu.Unlock()
		return newError(ReasonQueueFull,
			"generation queue full (%d waiters)", s.cfg.MaxQueue)
	}
	s.waiting++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}()
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctxErr(ctx.Err(), "waiting for generation slot")
	}
}

func (s *Service) release() { <-s.sem }

// ctxErr maps a context error to a distinguishable delivery error.
func ctxErr(err error, where string) error {
	if err == context.DeadlineExceeded {
		return newError(ReasonTimeout, "%s: %v", where, err)
	}
	return newError(ReasonCanceled, "%s: %v", where, err)
}

// decodeSourceCached decodes a source once per content hash.
func (s *Service) decodeSourceCached(src *Source) (image.Image, error) {
	s.mu.Lock()
	if img, ok := s.decoded[src.Hash]; ok {
		s.mu.Unlock()
		return img, nil
	}
	s.mu.Unlock()
	img, _, err := decodeSource(src)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.decodedN >= 32 { // bound memory: drop all, decode-on-demand refills
		s.decoded = map[string]image.Image{}
		s.decodedN = 0
	}
	s.decoded[src.Hash] = img
	s.decodedN++
	s.mu.Unlock()
	return img, nil
}
