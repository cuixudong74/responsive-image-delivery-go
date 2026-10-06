package delivery

import (
	"errors"
	"testing"
)

func TestNegotiateFormatPrefersEfficient(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	// 客户端接受任意格式：应选体积效率最高的 JPEG。
	f, reason, err := n.NegotiateFormat(ClientCapabilities{})
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if f != FormatJPEG {
		t.Fatalf("want jpeg, got %s", f)
	}
	if reason != ReasonWithinBudget {
		t.Fatalf("unexpected reason %s", reason)
	}
	// 通配声明同样如此。
	f, _, err = n.NegotiateFormat(ClientCapabilities{AcceptFormats: []string{"image/*"}})
	if err != nil || f != FormatJPEG {
		t.Fatalf("wildcard: got %s err %v", f, err)
	}
}

func TestNegotiateFormatFallback(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	// 客户端只声明能解码 PNG：必须回退到 PNG，不得返回 JPEG。
	f, reason, err := n.NegotiateFormat(ClientCapabilities{AcceptFormats: []string{"image/png"}})
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if f != FormatPNG {
		t.Fatalf("want png fallback, got %s", f)
	}
	if reason != ReasonFallbackFormat {
		t.Fatalf("want fallback reason, got %s", reason)
	}
}

func TestNegotiateFormatNoCommon(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	// 客户端只接受服务端不支持的 webp：必须报错而不是返回无法解码的数据。
	_, reason, err := n.NegotiateFormat(ClientCapabilities{AcceptFormats: []string{"image/webp"}})
	if !errors.Is(err, ErrNoCommonFormat) {
		t.Fatalf("want ErrNoCommonFormat, got %v", err)
	}
	if reason != ReasonNoCommonFormat {
		t.Fatalf("want no_common_format reason, got %s", reason)
	}
}

func TestSelectWithinBudget(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	caps := ClientCapabilities{
		ScreenWidthCSS: 640, DPR: 2, Network: Network4G,
	}
	sel, err := n.Select(1920, caps, func(s VariantSpec) (int64, error) {
		return int64(s.Width) * 100, nil // 确定性估计
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Spec.Width != 1280 {
		t.Fatalf("want width 1280 (640*2), got %d", sel.Spec.Width)
	}
	if !sel.WithinBudget || sel.Reason != ReasonWithinBudget {
		t.Fatalf("want within budget, got %+v", sel)
	}
}

func TestSelectDowngradeForBudget(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	// 预算只允许宽度 <= 480 的候选。
	n.budgets[Network2G] = Budget{MaxBytes: 480 * 100}
	caps := ClientCapabilities{ScreenWidthCSS: 1280, DPR: 1, Network: Network2G}
	sel, err := n.Select(1920, caps, func(s VariantSpec) (int64, error) {
		return int64(s.Width) * 100, nil
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Spec.Width != 480 {
		t.Fatalf("want downgrade to 480, got %d", sel.Spec.Width)
	}
	if !sel.WithinBudget || sel.Reason != ReasonDowngradedForBudget {
		t.Fatalf("want downgraded_for_budget, got %+v", sel)
	}
}

func TestSelectOverBudgetFloor(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	// 预算极小：没有任何候选满足，必须选最小规格并给出可区分原因。
	n.budgets[NetworkSaveData] = Budget{MaxBytes: 1}
	caps := ClientCapabilities{ScreenWidthCSS: 1280, DPR: 1, Network: NetworkSaveData}
	sel, err := n.Select(1920, caps, func(s VariantSpec) (int64, error) {
		return int64(s.Width) * 100, nil
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Spec.Width != 320 {
		t.Fatalf("want smallest candidate 320, got %d", sel.Spec.Width)
	}
	if sel.WithinBudget || sel.Reason != ReasonOverBudgetFloor {
		t.Fatalf("want over_budget_floor, got %+v", sel)
	}
}

func TestSelectDeterministic(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	caps := ClientCapabilities{ScreenWidthCSS: 500, DPR: 1.5, Network: Network3G}
	est := func(s VariantSpec) (int64, error) { return int64(s.Width) * 42, nil }
	first, err := n.Select(1600, caps, est)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := n.Select(1600, caps, est)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if got.Spec != first.Spec || got.Reason != first.Reason || got.WithinBudget != first.WithinBudget {
			t.Fatalf("non-deterministic selection: %+v vs %+v", got, first)
		}
	}
}

func TestSelectNoUpscale(t *testing.T) {
	n := NewNegotiator(DefaultRegistry(), DefaultConfig(), nil)
	caps := ClientCapabilities{ScreenWidthCSS: 4000, DPR: 2, Network: Network4G}
	sel, err := n.Select(800, caps, func(s VariantSpec) (int64, error) {
		return 1, nil
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Spec.Width > 800 {
		t.Fatalf("must not upscale beyond source width, got %d", sel.Spec.Width)
	}
}
