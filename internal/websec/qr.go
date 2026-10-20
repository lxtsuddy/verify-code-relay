package websec

import "rsc.io/qr"

// QRPNG renders text as a QR code PNG (error correction level M).
func QRPNG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	c.Scale = 6
	return c.PNG(), nil
}
