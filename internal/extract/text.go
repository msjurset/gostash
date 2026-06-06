package extract

import (
	"io"
	"strings"
)

// TextExtractor handles plain text content.
type TextExtractor struct{}

func (e *TextExtractor) Supports(mimeType string) bool {
	return strings.HasPrefix(mimeType, "text/plain") || mimeType == ""
}

func (e *TextExtractor) Extract(r io.Reader, mimeType string, opts Options) (*Result, error) {
	// Cap reading at 5MB to prevent memory exhaustion on massive text files
	limitReader := io.LimitReader(r, 5 * 1024 * 1024)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, err
	}
	text := string(data)

	// Use first line as title if short enough
	title := ""
	if idx := strings.IndexByte(text, '\n'); idx > 0 && idx < 120 {
		title = strings.TrimSpace(text[:idx])
	}

	return &Result{
		Text:     text,
		Title:    title,
		MimeType: "text/plain",
	}, nil
}
