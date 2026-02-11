package wireguard

import (
	"strings"
	"testing"
)

func TestGenerateQRCode(t *testing.T) {
	config := "[Interface]\nPrivateKey = xxx\nAddress = 10.42.100.1/32\n"

	png, err := GenerateQRCode(config, 256)
	if err != nil {
		t.Fatalf("failed to generate QR code: %v", err)
	}

	// Should return PNG data
	if len(png) == 0 {
		t.Error("QR code PNG should not be empty")
	}

	// PNG magic bytes
	if len(png) < 8 {
		t.Fatal("PNG data too short")
	}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for i := 0; i < 8; i++ {
		if png[i] != pngMagic[i] {
			t.Errorf("PNG magic byte mismatch at %d: got %x, want %x", i, png[i], pngMagic[i])
		}
	}
}

func TestGenerateQRCodeDataURL(t *testing.T) {
	config := "[Interface]\nPrivateKey = xxx\nAddress = 10.42.100.1/32\n"

	dataURL, err := GenerateQRCodeDataURL(config, 256)
	if err != nil {
		t.Fatalf("failed to generate QR code data URL: %v", err)
	}

	// Should be a data URL
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Error("QR code should be a PNG data URL")
	}

	// Should have base64 content
	base64Part := strings.TrimPrefix(dataURL, "data:image/png;base64,")
	if len(base64Part) == 0 {
		t.Error("QR code base64 content should not be empty")
	}
}

func TestGenerateQRCodeEmptyConfig(t *testing.T) {
	_, err := GenerateQRCode("", 256)
	if err == nil {
		t.Error("should error on empty config")
	}
}

func TestGenerateQRCodeDifferentSizes(t *testing.T) {
	config := "[Interface]\nPrivateKey = xxx\nAddress = 10.42.100.1/32\n"

	sizes := []int{128, 256, 512}
	var prevLen int

	for _, size := range sizes {
		png, err := GenerateQRCode(config, size)
		if err != nil {
			t.Fatalf("failed to generate QR code at size %d: %v", size, err)
		}

		// Larger sizes should generally produce larger images
		if prevLen > 0 && len(png) <= prevLen {
			t.Logf("Warning: size %d produced %d bytes, previous was %d", size, len(png), prevLen)
		}
		prevLen = len(png)
	}
}
