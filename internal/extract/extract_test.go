package extract

import (
	"strings"
	"testing"
)

func TestTextExtractor(t *testing.T) {
	e := &TextExtractor{}

	tests := []struct {
		name    string
		input   string
		mime    string
		wantTxt string
	}{
		{
			name:    "plain text",
			input:   "Hello world\nSecond line",
			mime:    "text/plain",
			wantTxt: "Hello world\nSecond line",
		},
		{
			name:    "empty",
			input:   "",
			mime:    "text/plain",
			wantTxt: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !e.Supports(tt.mime) {
				t.Error("should support", tt.mime)
			}
			result, err := e.Extract(strings.NewReader(tt.input), tt.mime)
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != tt.wantTxt {
				t.Errorf("text = %q, want %q", result.Text, tt.wantTxt)
			}
		})
	}
}

func TestHTMLExtractorSupports(t *testing.T) {
	e := &HTMLExtractor{}

	tests := []struct {
		mime string
		want bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"application/xhtml+xml", true},
		{"text/plain", false},
		{"application/json", false},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := e.Supports(tt.mime); got != tt.want {
				t.Errorf("Supports(%q) = %v, want %v", tt.mime, got, tt.want)
			}
		})
	}
}

func TestDetectMIME(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"html file", "page.html", "text/html"},
		{"text file", "notes.txt", "text/plain"},
		{"pdf file", "doc.pdf", "application/pdf"},
		{"png file", "photo.png", "image/png"},
		{"unknown", "file.xyz", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectMIME(nil, tt.filename)
			if got != tt.want {
				t.Errorf("DetectMIME(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestSuggestTags(t *testing.T) {
	tests := []struct {
		mime string
		want int
	}{
		{"image/png", 1},
		{"application/pdf", 2},
		{"text/html", 1},
		{"text/plain", 0},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			tags := SuggestTags(tt.mime)
			if len(tags) != tt.want {
				t.Errorf("SuggestTags(%q) = %v (len %d), want len %d", tt.mime, tags, len(tags), tt.want)
			}
		})
	}
}

func TestRunFallback(t *testing.T) {
	result, err := Run(strings.NewReader("plain text content"), "application/unknown")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "plain text content" {
		t.Errorf("text = %q, want %q", result.Text, "plain text content")
	}
}
