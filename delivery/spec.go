package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// VariantSpec 描述一个派生规格：目标宽度、像素密度、格式与质量。
// 派生结果只由源图内容、本规格与生成配置决定。
type VariantSpec struct {
	Width   int    // 目标像素宽（已含 DPR 换算）
	Format  Format // 目标编码格式
	Quality int    // 编码质量 1-100
}

// String 返回规格的稳定字符串表示，用于日志与缓存键。
func (s VariantSpec) String() string {
	return fmt.Sprintf("w%d-%s-q%d", s.Width, mimeShort(s.Format), s.Quality)
}

func mimeShort(f Format) string {
	if i := strings.IndexByte(string(f), '/'); i >= 0 {
		return string(f[i+1:])
	}
	return string(f)
}

// LadderConfig 控制候选规格阶梯的生成。
type LadderConfig struct {
	// Widths 是候选宽度阶梯（CSS 像素，升序），为空时使用默认值。
	Widths []int
	// DPRs 是候选像素密度（升序），为空时使用 {1,2}。
	DPRs []float64
	// Quality 是编码质量，0 表示默认 80。
	Quality int
	// MaxSourceUpscale 允许的最大放大倍数，<=0 表示不允许放大。
	MaxSourceUpscale float64
}

// Config 是生成相关配置，参与缓存键计算；配置变化会使旧缓存键失效。
type Config struct {
	Ladder LadderConfig
}

// DefaultConfig 返回默认生成配置。
func DefaultConfig() Config {
	return Config{Ladder: LadderConfig{
		Widths:  []int{320, 480, 640, 768, 1024, 1280, 1600, 1920},
		DPRs:    []float64{1, 2},
		Quality: 80,
	}}
}

// Hash 返回配置的稳定哈希，用于缓存键与失效判断。
func (c Config) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "q=%d;up=%g;w=", c.Ladder.Quality, c.Ladder.MaxSourceUpscale)
	for _, w := range c.Ladder.Widths {
		fmt.Fprintf(h, "%d,", w)
	}
	fmt.Fprintf(h, ";d=")
	for _, d := range c.Ladder.DPRs {
		fmt.Fprintf(h, "%g,", d)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c Config) quality() int { return clampQuality(c.Ladder.Quality) }

func (c Config) widths() []int {
	if len(c.Ladder.Widths) > 0 {
		return c.Ladder.Widths
	}
	return DefaultConfig().Ladder.Widths
}

func (c Config) dprs() []float64 {
	if len(c.Ladder.DPRs) > 0 {
		return c.Ladder.DPRs
	}
	return DefaultConfig().Ladder.DPRs
}

// Candidates 根据源图宽度枚举候选规格（按目标宽度升序，宽度唯一）。
// 候选宽度 = 阶梯宽度 × 候选 DPR，去重并截断到放大上限；
// 源图原始宽度始终包含在内，保证总有可交付规格。
func (c Config) Candidates(srcWidth int, f Format) []VariantSpec {
	maxW := srcWidth
	if c.Ladder.MaxSourceUpscale > 0 {
		maxW = int(float64(srcWidth) * c.Ladder.MaxSourceUpscale)
	}
	seen := map[int]bool{}
	var ws []int
	for _, w := range c.widths() {
		for _, d := range c.dprs() {
			px := int(float64(w)*d + 0.5)
			if px <= 0 || px > maxW {
				continue
			}
			if !seen[px] {
				seen[px] = true
				ws = append(ws, px)
			}
		}
	}
	if !seen[srcWidth] {
		ws = append(ws, srcWidth)
	}
	sort.Ints(ws)
	out := make([]VariantSpec, len(ws))
	for i, w := range ws {
		out[i] = VariantSpec{Width: w, Format: f, Quality: c.quality()}
	}
	return out
}
