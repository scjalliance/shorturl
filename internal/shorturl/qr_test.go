package shorturl

import (
	"bytes"
	"image/png"
	"testing"
)

func TestQRPNG(t *testing.T) {
	data, err := QRPNG("https://example.com/qr/demo")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() || b.Dx()%qrScale != 0 || b.Dx() < 21*qrScale {
		t.Errorf("unexpected image size %dx%d", b.Dx(), b.Dy())
	}
}
