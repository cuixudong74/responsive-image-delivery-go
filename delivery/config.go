package delivery

import "time"

// Config tunes negotiation, generation and resource limits.
// A zero Config is invalid; use DefaultConfig as a starting point.
type Config struct {
	// Widths is the ascending ladder of candidate derivative widths.
	Widths []int
	// QualityDefault is the encoder quality under normal conditions.
	QualityDefault int
	// QualitySaveData is the encoder quality when SaveData is set.
	QualitySaveData int
	// BudgetNormal is the default per-image byte budget.
	BudgetNormal int
	// BudgetSaveData is the byte budget when SaveData is set.
	BudgetSaveData int
	// BudgetsByEffectiveType overrides BudgetNormal per network class.
	BudgetsByEffectiveType map[string]int
	// MaxConcurrentGenerations bounds parallel derivative generations.
	MaxConcurrentGenerations int
	// MaxQueue bounds the number of requests waiting for a generation slot.
	MaxQueue int
	// CacheMaxBytes bounds total cached derivative bytes (LRU eviction).
	CacheMaxBytes int64
	// GenerationTimeout bounds a single generation.
	GenerationTimeout time.Duration
	// StatsCapacity is the number of recent events kept for inspection.
	StatsCapacity int
}

// DefaultConfig returns a sane in-process configuration.
func DefaultConfig() Config {
	return Config{
		Widths:          []int{160, 320, 640, 960, 1280, 1920},
		QualityDefault:  80,
		QualitySaveData: 55,
		BudgetNormal:    256 * 1024,
		BudgetSaveData:  64 * 1024,
		BudgetsByEffectiveType: map[string]int{
			"slow-2g": 24 * 1024,
			"2g":      48 * 1024,
			"3g":      128 * 1024,
			"4g":      512 * 1024,
		},
		MaxConcurrentGenerations: 4,
		MaxQueue:                 64,
		CacheMaxBytes:            32 << 20,
		GenerationTimeout:        10 * time.Second,
		StatsCapacity:            256,
	}
}
