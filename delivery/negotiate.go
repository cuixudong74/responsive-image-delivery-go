package delivery

// NegotiateFormat picks the most efficient format the client declares
// it can decode. Modern (smaller) formats win when supported; the
// universal fallback (JPEG) is chosen otherwise. It never returns a
// format the client did not declare: if the client accepts nothing we
// can serve, a distinguishable ReasonNoCommonFormat error is returned.
func NegotiateFormat(caps ClientCapabilities) (Format, error) {
	accepted := map[Format]bool{}
	for _, f := range caps.AcceptFormats {
		if codecFor(f) != nil {
			accepted[f] = true
		}
	}
	// Candidates ordered by efficiency (smallest typical size first).
	candidates := []Format{FormatRIMG, FormatJPEG, FormatPNG}
	for _, f := range candidates {
		if accepted[f] {
			return f, nil
		}
	}
	return "", newError(ReasonNoCommonFormat,
		"client accepts %v, server can serve jpeg/png/rimg", caps.AcceptFormats)
}

// budgetFor derives the byte/quality budget from capabilities and
// config. Precedence: explicit MaxBytes override > SaveData budget >
// per-network-class budget > default budget.
func budgetFor(caps ClientCapabilities, cfg Config) Budget {
	maxBytes := cfg.BudgetNormal
	if b, ok := cfg.BudgetsByEffectiveType[caps.EffectiveType]; ok && b > 0 {
		maxBytes = b
	}
	quality := cfg.QualityDefault
	if caps.SaveData {
		maxBytes = cfg.BudgetSaveData
		quality = cfg.QualitySaveData
	}
	if caps.MaxBytes > 0 {
		maxBytes = caps.MaxBytes
	}
	return Budget{MaxBytes: maxBytes, MaxQuality: quality}
}
