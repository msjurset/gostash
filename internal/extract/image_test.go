package extract

import (
	"strings"
	"testing"
)

func TestImageExtractorSupports(t *testing.T) {
	e := &ImageExtractor{}

	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"text/plain", false},
		{"application/pdf", false},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := e.Supports(tt.mime); got != tt.want {
				t.Errorf("Supports(%q) = %v, want %v", tt.mime, got, tt.want)
			}
		})
	}
}

func TestTesseractAvailable(t *testing.T) {
	// Just verify the function doesn't panic
	_ = tesseractAvailable()
}

func TestImageExtractWithoutTesseract(t *testing.T) {
	if tesseractAvailable() {
		t.Skip("tesseract is installed, skip fallback test")
	}

	e := &ImageExtractor{}

	result, err := e.Extract(
		strings.NewReader("fake image data"),
		"image/png",
	)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// Without tesseract, should still return a result with empty text
	if result.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", result.MimeType)
	}
	if result.Text != "" {
		t.Errorf("expected empty text without tesseract, got %q", result.Text)
	}
}
