package extract

import (
	"io"
	"strings"

	readability "github.com/go-shiori/go-readability"
)

// HTMLExtractor uses go-readability to extract readable text from HTML.
type HTMLExtractor struct{}

func (e *HTMLExtractor) Supports(mimeType string) bool {
	return strings.Contains(mimeType, "html")
}

func (e *HTMLExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	// go-readability expects a URL for resolving relative links; empty is fine for extraction
	article, err := readability.FromReader(r, nil)
	if err != nil {
		// Fall back to raw text on parse failure
		return (&TextExtractor{}).Extract(r, mimeType)
	}

	return &Result{
		Text:     article.TextContent,
		Title:    article.Title,
		MimeType: "text/html",
	}, nil
}
