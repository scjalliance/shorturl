package shorturl

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"
)

// decodeQR reads the text encoded in a PNG QR code, failing the test if the
// image is not a PNG or does not contain a readable QR code.
func decodeQR(t *testing.T, data []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a PNG: %v", err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatalf("binarizing image: %v", err)
	}
	result, err := zxqr.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("image does not decode as a QR code: %v", err)
	}
	return result.GetText()
}

func TestQRPNG(t *testing.T) {
	const content = "https://example.com/qr/demo"
	data, err := QRPNG(content)
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
	if got := decodeQR(t, data); got != content {
		t.Errorf("QR code encodes %q, want %q", got, content)
	}
}
