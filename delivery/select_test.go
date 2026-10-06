package delivery

import (
	"context"
	"strings"
	"testing"
)

func deliver(t *testing.T, svc *Service, id string, caps ClientCapabilities) *Response {
	t.Helper()
	resp, err := svc.Deliver(context.Background(), Request{SourceID: id, Caps: caps})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	return resp
}

func TestSelectionIsUniqueAndExplainable(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1600, 1200)))
	caps := ClientCapabilities{AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g"}
	r1 := deliver(t, svc, "photo", caps)
	r2 := deliver(t, svc, "photo", caps)
	if r1.Selection.Spec != r2.Selection.Spec {
		t.Fatalf("same input selected different specs: %+v vs %+v", r1.Selection.Spec, r2.Selection.Spec)
	}
	if r1.Selection.Explanation == "" || !strings.Contains(r1.Selection.Explanation, "budget=") {
		t.Fatalf("missing explanation: %q", r1.Selection.Explanation)
	}
	// 400 CSS px @2x => target 800 => smallest covering width is 960.
	if r1.Selection.Spec.Width != 960 {
		t.Fatalf("want width 960, got %d (%s)", r1.Selection.Spec.Width, r1.Selection.Explanation)
	}
	if !r1.Selection.WithinBudget {
		t.Fatalf("960px rimg should fit 4g budget: %s", r1.Selection.Explanation)
	}
}

func TestSelectionStepsDownToFitBudget(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1600, 1200)))
	// Tiny budget forces stepping down the ladder.
	caps := ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2,
		MaxBytes: 6000,
	}
	resp := deliver(t, svc, "photo", caps)
	if !resp.Selection.WithinBudget {
		t.Fatalf("expected a fitting variant: %s", resp.Selection.Explanation)
	}
	if len(resp.Data) > 6000 {
		t.Fatalf("delivered %dB over 6000B budget", len(resp.Data))
	}
	if resp.Selection.Spec.Width >= 960 {
		t.Fatalf("should have stepped down from 960: %s", resp.Selection.Explanation)
	}
}

func TestOverBudgetReturnsSmallestWithDistinguishableReason(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1600, 1200)))
	// Budget below even the smallest variant: must not silently
	// return an over-budget result.
	resp := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2,
		MaxBytes: 10,
	})
	if resp.Selection.WithinBudget {
		t.Fatal("impossible: 10B budget cannot be within budget")
	}
	if resp.Selection.Reason != ReasonOverBudgetSmallest {
		t.Fatalf("want ReasonOverBudgetSmallest, got %s", resp.Selection.Reason)
	}
	if resp.Selection.Spec.Width != 160 { // smallest configured width
		t.Fatalf("want smallest variant, got %s", resp.Selection.Explanation)
	}
	snap := svc.Stats().Snapshot()
	if snap.OverBudget == 0 {
		t.Fatal("over-budget delivery not counted in stats")
	}
}

func TestSaveDataLowersBudgetAndQuality(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1600, 1200)))
	normal := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g",
	})
	saver := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g", SaveData: true,
	})
	if saver.Selection.Spec.Quality >= normal.Selection.Spec.Quality {
		t.Fatalf("save-data quality %d not below normal %d",
			saver.Selection.Spec.Quality, normal.Selection.Spec.Quality)
	}
	if saver.Selection.Budget.MaxBytes >= normal.Selection.Budget.MaxBytes {
		t.Fatalf("save-data budget not lower: %+v vs %+v", saver.Selection.Budget, normal.Selection.Budget)
	}
}

func TestSlowNetworkGetsSmallerVariant(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(1600, 1200)))
	fast := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "4g",
	})
	slow := deliver(t, svc, "photo", ClientCapabilities{
		AcceptFormats: allFormats, ScreenWidth: 400, DevicePixelRatio: 2, EffectiveType: "slow-2g",
	})
	if len(slow.Data) >= len(fast.Data) {
		t.Fatalf("slow-2g got %dB, 4g got %dB", len(slow.Data), len(fast.Data))
	}
}
