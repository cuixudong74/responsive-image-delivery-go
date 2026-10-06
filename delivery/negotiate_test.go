package delivery

import (
	"context"
	"testing"
)

func TestNegotiatePrefersModernFormat(t *testing.T) {
	f, err := NegotiateFormat(ClientCapabilities{AcceptFormats: allFormats})
	if err != nil {
		t.Fatal(err)
	}
	if f != FormatRIMG {
		t.Fatalf("want modern format rimg, got %s", f)
	}
}

func TestNegotiateFallsBackToUniversal(t *testing.T) {
	// Client cannot decode the modern format: must get JPEG.
	f, err := NegotiateFormat(ClientCapabilities{AcceptFormats: []Format{FormatJPEG, FormatPNG}})
	if err != nil {
		t.Fatal(err)
	}
	if f != FormatJPEG {
		t.Fatalf("want jpeg fallback, got %s", f)
	}
}

func TestNegotiateNeverReturnsUndeclaredFormat(t *testing.T) {
	cases := [][]Format{
		{FormatPNG},
		{FormatJPEG},
		{FormatRIMG},
		{FormatPNG, FormatRIMG},
	}
	for _, accepted := range cases {
		f, err := NegotiateFormat(ClientCapabilities{AcceptFormats: accepted})
		if err != nil {
			t.Fatalf("accepts %v: %v", accepted, err)
		}
		ok := false
		for _, a := range accepted {
			if a == f {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("returned %s which client did not declare (accepts %v)", f, accepted)
		}
	}
}

func TestNegotiateNoCommonFormat(t *testing.T) {
	_, err := NegotiateFormat(ClientCapabilities{AcceptFormats: []Format{"tiff", "bmp"}})
	if err == nil {
		t.Fatal("want error")
	}
	e, ok := err.(*Error)
	if !ok || e.Reason != ReasonNoCommonFormat {
		t.Fatalf("want ReasonNoCommonFormat, got %v", err)
	}
}

// The delivered bytes must always be decodable with the negotiated
// format the client declared.
func TestDeliveredBytesMatchDeclaredCapability(t *testing.T) {
	svc, store := newTestService(t, nil)
	store.Put("photo", encodePNG(t, sampleImage(800, 600)))
	for _, accepted := range [][]Format{allFormats, {FormatJPEG}, {FormatPNG}, {FormatJPEG, FormatPNG}} {
		resp, err := svc.Deliver(context.Background(), Request{
			SourceID: "photo",
			Caps:     ClientCapabilities{AcceptFormats: accepted, ScreenWidth: 400, DevicePixelRatio: 2},
		})
		if err != nil {
			t.Fatalf("accepts %v: %v", accepted, err)
		}
		// Client decodes with a format it declared.
		if _, err := codecFor(resp.Selection.Spec.Format).Decode(resp.Data); err != nil {
			t.Fatalf("accepts %v: delivered undecodable bytes: %v", accepted, err)
		}
		declared := false
		for _, a := range accepted {
			if a == resp.Selection.Spec.Format {
				declared = true
			}
		}
		if !declared {
			t.Fatalf("delivered %s not in declared %v", resp.Selection.Spec.Format, accepted)
		}
	}
}

func TestModernFormatIsSmaller(t *testing.T) {
	img := sampleImage(320, 240)
	rimg, err := codecFor(FormatRIMG).Encode(img, 80)
	if err != nil {
		t.Fatal(err)
	}
	pngB, err := codecFor(FormatPNG).Encode(img, 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(rimg) >= len(pngB) {
		t.Fatalf("modern format %dB not smaller than png %dB", len(rimg), len(pngB))
	}
	// rimg roundtrip must decode to the same dimensions.
	dec, err := codecFor(FormatRIMG).Decode(rimg)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() != 320 || dec.Bounds().Dy() != 240 {
		t.Fatalf("roundtrip dims %v", dec.Bounds())
	}
}
