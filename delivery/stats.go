package delivery

import "sync/atomic"

// Stats 记录协商与生成的可观测统计。
type Stats struct {
	Requests      atomic.Int64 // 总请求数
	CacheHits     atomic.Int64 // 复用已有结果（含等待并发同键）
	Generations   atomic.Int64 // 实际触发生成次数
	GenFailures   atomic.Int64 // 生成失败次数
	OverBudget    atomic.Int64 // 结果超出预算次数
	Rejected      atomic.Int64 // 因容量被拒绝次数
	Invalidations atomic.Int64 // 失效的缓存条目数
}

// Snapshot 是 Stats 的一致性快照。
type Snapshot struct {
	Requests      int64
	CacheHits     int64
	Generations   int64
	GenFailures   int64
	OverBudget    int64
	Rejected      int64
	Invalidations int64
}

// Snapshot 返回当前统计快照。
func (s *Stats) Snapshot() Snapshot {
	return Snapshot{
		Requests:      s.Requests.Load(),
		CacheHits:     s.CacheHits.Load(),
		Generations:   s.Generations.Load(),
		GenFailures:   s.GenFailures.Load(),
		OverBudget:    s.OverBudget.Load(),
		Rejected:      s.Rejected.Load(),
		Invalidations: s.Invalidations.Load(),
	}
}
