package shorturl

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

// qrScale is the pixel size of one QR module. The original implementation
// used the node "qrcode" package defaults: error correction level M, a
// four module quiet zone, and four pixels per module.
const qrScale = 4

// QRPNG renders content as a PNG QR code with the same geometry the original
// implementation produced.
func QRPNG(content string) ([]byte, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("encoding qr code: %w", err)
	}
	// Bitmap includes the four module quiet zone, so the whole image is an
	// exact multiple of the module size.
	size := len(q.Bitmap()) * qrScale
	png, err := q.PNG(size)
	if err != nil {
		return nil, fmt.Errorf("rendering qr png: %w", err)
	}
	return png, nil
}
