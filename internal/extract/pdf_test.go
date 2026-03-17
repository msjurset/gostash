package extract

import (
	"os"
	"strings"
	"testing"
)

func TestPDFExtractorSupports(t *testing.T) {
	e := &PDFExtractor{}

	tests := []struct {
		mime string
		want bool
	}{
		{"application/pdf", true},
		{"text/plain", false},
		{"image/png", false},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := e.Supports(tt.mime); got != tt.want {
				t.Errorf("Supports(%q) = %v, want %v", tt.mime, got, tt.want)
			}
		})
	}
}

func TestPDFExtract(t *testing.T) {
	f, err := os.Open("testdata/sample.pdf")
	if err != nil {
		t.Skip("testdata/sample.pdf not found, skipping PDF extraction test")
	}
	defer f.Close()

	e := &PDFExtractor{}
	result, err := e.Extract(f, "application/pdf")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if result.MimeType != MIMEPDF {
		t.Errorf("mime = %q, want %q", result.MimeType, MIMEPDF)
	}
	if !strings.Contains(strings.Join(result.Tags, ","), "pdf") {
		t.Error("expected 'pdf' in tags")
	}
}
