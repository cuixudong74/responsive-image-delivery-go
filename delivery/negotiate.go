package delivery

import (
	"errors"
	"strings"
)

// NetworkClass 表示客户端网络条件。
type NetworkClass string

const (
	NetworkSaveData NetworkClass = "save-data"
	Network2G       NetworkClass = "2g"
	Network3G       NetworkClass = "3g"
	Network4G       NetworkClass = "4g"
	NetworkUnknown  NetworkClass = "unknown"
)

// ClientCapabilities 是客户端声明的能力与上下文。
type ClientCapabilities struct {
	// AcceptFormats 按客户端偏好降序列出可解码格式的 MIME 类型；
	// 支持 "*/*" 与 "image/*" 通配；空表示接受任意格式。
	AcceptFormats []string
	// ScreenWidthCSS 终端屏宽（CSS 像素），<=0 表示未知。
	ScreenWidthCSS int
	// DPR 设备像素比，<=0 视为 1。
	DPR float64
	// Network 网络条件，用于体积预算。
	Network NetworkClass
}

// Budget 是一次请求允许的最大派生体积（字节）。
type Budget struct {
	MaxBytes int64
}

// ReasonCode 解释协商/选择结果，便于观测与排障。
type ReasonCode string

const (
	ReasonWithinBudget        ReasonCode = "within_budget"         // 命中预算内的最优规格
	ReasonDowngradedForBudget ReasonCode = "downgraded_for_budget" // 理想规格超预算，降级到预算内较小规格
	ReasonOverBudgetFloor     ReasonCode = "over_budget_floor"     // 无规格满足预算，回退到最小规格（超预算交付）
	ReasonFallbackFormat      ReasonCode = "fallback_format"       // 现代格式不被接受，回退通用格式
	ReasonNoCommonFormat      ReasonCode = "no_common_format"      // 客户端与服务端无共同格式
)

// ErrNoCommonFormat 表示客户端声明的格式与服务端能力无交集。
var ErrNoCommonFormat = errors.New("delivery: no common format with client")

// Selection 是一次协商选择的结果。
type Selection struct {
	Spec         VariantSpec
	Reason       ReasonCode
	WithinBudget bool
	Budget       Budget
}

// Negotiator 负责格式协商与预算内的规格选择。
type Negotiator struct {
	reg     *Registry
	cfg     Config
	budgets map[NetworkClass]Budget
}

// DefaultBudgets 返回按网络条件的默认体积预算。
func DefaultBudgets() map[NetworkClass]Budget {
	return map[NetworkClass]Budget{
		NetworkSaveData: {MaxBytes: 50 * 1024},
		Network2G:       {MaxBytes: 100 * 1024},
		Network3G:       {MaxBytes: 250 * 1024},
		Network4G:       {MaxBytes: 1024 * 1024},
		NetworkUnknown:  {MaxBytes: 1024 * 1024},
	}
}

// NewNegotiator 构造协商器。budgets 为空时使用默认预算表。
func NewNegotiator(reg *Registry, cfg Config, budgets map[NetworkClass]Budget) *Negotiator {
	if len(budgets) == 0 {
		budgets = DefaultBudgets()
	}
	return &Negotiator{reg: reg, cfg: cfg, budgets: budgets}
}

// BudgetFor 返回某网络条件对应的预算；未知条件回落到 NetworkUnknown 的预算。
func (n *Negotiator) BudgetFor(nc NetworkClass) Budget {
	if b, ok := n.budgets[nc]; ok {
		return b
	}
	return n.budgets[NetworkUnknown]
}

// NegotiateFormat 按客户端声明与服务端效率选择格式：
// 在客户端接受的格式中，选服务端效率最高（体积通常最小）的一个；
// 客户端只接受通用格式时回退并标记 ReasonFallbackFormat；
// 无交集时返回 ErrNoCommonFormat，绝不返回客户端无法解码的格式。
func (n *Negotiator) NegotiateFormat(caps ClientCapabilities) (Format, ReasonCode, error) {
	ranked := n.reg.FormatsByEfficiency()
	if len(ranked) == 0 {
		return "", ReasonNoCommonFormat, ErrNoCommonFormat
	}
	acceptAll := len(caps.AcceptFormats) == 0
	if !acceptAll {
		for _, a := range caps.AcceptFormats {
			a = strings.TrimSpace(strings.ToLower(a))
			if a == "*/*" || a == "image/*" {
				acceptAll = true
				break
			}
		}
	}
	if acceptAll {
		// 客户端未限制格式：直接给体积效率最高的现代格式。
		return ranked[0].Format(), ReasonWithinBudget, nil
	}
	accepted := map[Format]bool{}
	for _, a := range caps.AcceptFormats {
		accepted[Format(strings.TrimSpace(strings.ToLower(a)))] = true
	}
	for i, c := range ranked {
		if accepted[c.Format()] {
			if i == 0 {
				return c.Format(), ReasonWithinBudget, nil
			}
			return c.Format(), ReasonFallbackFormat, nil
		}
	}
	return "", ReasonNoCommonFormat, ErrNoCommonFormat
}

// Select 在格式确定后，按屏宽/密度/网络预算选择唯一规格：
//  1. 理想宽度 = 屏宽 × DPR（屏宽未知时取源图宽度）；
//  2. 在候选中取不小于理想宽度的最小规格（没有则取最大候选）；
//  3. 若其估计体积超预算，则向更小的候选降级，直到落入预算；
//  4. 没有任何候选满足预算时，选择体积最小的候选并标记
//     ReasonOverBudgetFloor（WithinBudget=false），绝不静默超预算。
//
// estimate 由调用方注入（通常为确定性的体积启发式），相同输入下结果唯一。
func (n *Negotiator) Select(srcWidth int, caps ClientCapabilities, estimate func(VariantSpec) (int64, error)) (Selection, error) {
	format, fmtReason, err := n.NegotiateFormat(caps)
	if err != nil {
		return Selection{}, err
	}
	cands := n.cfg.Candidates(srcWidth, format)
	if len(cands) == 0 {
		return Selection{}, errors.New("delivery: no candidate variants")
	}
	budget := n.BudgetFor(caps.Network)

	dpr := caps.DPR
	if dpr <= 0 {
		dpr = 1
	}
	need := srcWidth
	if caps.ScreenWidthCSS > 0 {
		need = int(float64(caps.ScreenWidthCSS)*dpr + 0.5)
	}
	// 理想规格：不小于 need 的最小候选，否则最大候选。
	ideal := len(cands) - 1
	for i, c := range cands {
		if c.Width >= need {
			ideal = i
			break
		}
	}

	sel := Selection{Budget: budget}
	if fmtReason == ReasonFallbackFormat {
		sel.Reason = ReasonFallbackFormat
	}
	// 从理想规格向下寻找第一个落入预算的候选。
	for i := ideal; i >= 0; i-- {
		est, err := estimate(cands[i])
		if err != nil {
			return Selection{}, err
		}
		if budget.MaxBytes <= 0 || est <= budget.MaxBytes {
			sel.Spec = cands[i]
			sel.WithinBudget = true
			if i < ideal && sel.Reason == "" {
				sel.Reason = ReasonDowngradedForBudget
			}
			if sel.Reason == "" {
				sel.Reason = ReasonWithinBudget
			}
			return sel, nil
		}
	}
	// 无候选满足预算：按既定策略取最小规格，明确标记超预算。
	sel.Spec = cands[0]
	sel.WithinBudget = false
	sel.Reason = ReasonOverBudgetFloor
	return sel, nil
}
