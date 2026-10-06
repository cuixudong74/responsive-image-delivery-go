package delivery

import (
	"fmt"
	"math"
	"sort"
)

// targetWidth maps terminal capabilities to the physical pixel width
// the image should cover.
func targetWidth(caps ClientCapabilities) int {
	if caps.ScreenWidth <= 0 {
		return 0 // unconstrained: caller uses the largest candidate
	}
	dpr := caps.DevicePixelRatio
	if dpr <= 0 {
		dpr = 1
	}
	return int(math.Ceil(float64(caps.ScreenWidth) * dpr))
}

// candidateLadder returns the deterministic, deduplicated, descending
// list of candidate specs to try for one request, best first. The
// same inputs always produce the same ladder, which makes the final
// selection unique and explainable.
func candidateLadder(srcW, srcH int, format Format, caps ClientCapabilities, cfg Config) []VariantSpec {
	if srcW <= 0 || srcH <= 0 {
		return nil
	}
	widths := append([]int(nil), cfg.Widths...)
	if len(widths) == 0 {
		widths = []int{srcW}
	}
	sort.Ints(widths)
	target := targetWidth(caps)
	// Primary: smallest configured width covering the target; if none
	// covers it (or the target is unconstrained), the largest width.
	primary := widths[len(widths)-1]
	for _, w := range widths {
		if target > 0 && w >= target {
			primary = w
			break
		}
	}
	// Never upscale beyond the source.
	if primary > srcW {
		primary = srcW
	}
	quality := budgetFor(caps, cfg).MaxQuality
	var ladder []VariantSpec
	seen := map[int]bool{}
	for _, w := range widths {
		if w > primary {
			continue
		}
		if w > srcW {
			w = srcW
		}
		if w <= 0 || seen[w] {
			continue
		}
		seen[w] = true
		h := int(math.Round(float64(w) * float64(srcH) / float64(srcW)))
		if h < 1 {
			h = 1
		}
		ladder = append(ladder, VariantSpec{Width: w, Height: h, Format: format, Quality: quality})
	}
	// Descending: primary (largest) first, smallest last.
	for i, j := 0, len(ladder)-1; i < j; i, j = i+1, j-1 {
		ladder[i], ladder[j] = ladder[j], ladder[i]
	}
	return ladder
}

// explainSelection renders a human-readable decision path.
func explainSelection(caps ClientCapabilities, budget Budget, spec VariantSpec, size int, within bool, tried int) string {
	fit := "within"
	if !within {
		fit = "over"
	}
	return fmt.Sprintf("target=%dpx screen=%d dpr=%.2g net=%q saveData=%v budget=%dB; "+
		"tried=%d spec=%dx%d@%d/%s size=%dB (%s budget)",
		targetWidth(caps), caps.ScreenWidth, caps.DevicePixelRatio, caps.EffectiveType,
		caps.SaveData, budget.MaxBytes, tried, spec.Width, spec.Height, spec.Quality,
		spec.Format, size, fit)
}
