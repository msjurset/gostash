package extract

import (
	"os"
	"strings"
	"testing"
)

func TestDocxExtractorSupports(t *testing.T) {
	e := &DocxExtractor{}

	tests := []struct {
		mime string
		want bool
	}{
		{MIMEDocx, true},
		{"application/msword", true},
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

func TestDocxExtract(t *testing.T) {
	f, err := os.Open("testdata/sample.docx")
	if err != nil {
		t.Skip("testdata/sample.docx not found, skipping DOCX extraction test")
	}
	defer f.Close()

	e := &DocxExtractor{}
	result, err := e.Extract(f, MIMEDocx)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if result.MimeType != MIMEDocx {
		t.Errorf("mime = %q, want %q", result.MimeType, MIMEDocx)
	}
	if !strings.Contains(strings.Join(result.Tags, ","), "docx") {
		t.Error("expected 'docx' in tags")
	}
}
