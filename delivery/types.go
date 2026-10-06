// Package delivery provides in-process responsive image delivery:
// content negotiation, budget-aware variant selection, deterministic
// derivative generation, deduplicated caching and backpressure.
package delivery

import "fmt"

// Format identifies an image wire format.
type Format string

const (
	FormatJPEG Format = "jpeg" // universal fallback
	FormatPNG  Format = "png"  // lossless, largest
	FormatRIMG Format = "rimg" // in-process modern format, smallest
)

// ReasonCode explains a selection outcome or a failure in a
// machine-distinguishable way.
type ReasonCode string

const (
	ReasonWithinBudget       ReasonCode = "within_budget"
	ReasonOverBudgetSmallest ReasonCode = "over_budget_smallest"
	ReasonNoCommonFormat     ReasonCode = "no_common_format"
	ReasonSourceNotFound     ReasonCode = "source_not_found"
	ReasonQueueFull          ReasonCode = "queue_full"
	ReasonGenerationFailed   ReasonCode = "generation_failed"
	ReasonCanceled           ReasonCode = "canceled"
	ReasonTimeout            ReasonCode = "timeout"
	ReasonInvalidRequest     ReasonCode = "invalid_request"
)

// Error is a typed failure with a distinguishable Reason.
type Error struct {
	Reason ReasonCode
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Reason, e.Msg) }

func newError(r ReasonCode, format string, args ...any) *Error {
	return &Error{Reason: r, Msg: fmt.Sprintf(format, args...)}
}

// ClientCapabilities describes what the client can decode and the
// terminal/network conditions of the request.
type ClientCapabilities struct {
	// AcceptFormats lists every format the client can decode.
	AcceptFormats []Format
	// ScreenWidth is the layout width in CSS pixels the image will occupy.
	ScreenWidth int
	// DevicePixelRatio maps CSS pixels to physical pixels (>= 1 typical).
	DevicePixelRatio float64
	// EffectiveType is a network class: "slow-2g","2g","3g","4g".
	EffectiveType string
	// SaveData requests aggressive byte savings.
	SaveData bool
	// MaxBytes overrides the derived byte budget when > 0.
	MaxBytes int
}

// VariantSpec fully determines a derivative together with the source
// content and the generator configuration.
type VariantSpec struct {
	Width   int
	Height  int
	Format  Format
	Quality int
}

// Budget constrains a delivered variant.
type Budget struct {
	MaxBytes   int
	MaxQuality int
}

// Selection is the explainable outcome of variant selection.
type Selection struct {
	Spec         VariantSpec
	Budget       Budget
	WithinBudget bool
	Reason       ReasonCode
	Explanation  string
}
