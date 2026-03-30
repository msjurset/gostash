package extract

import (
	"io"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
)

// HTMLExtractor uses go-readability to extract readable text from HTML
// and converts it to Markdown for structured display.
type HTMLExtractor struct{}

func (e *HTMLExtractor) Supports(mimeType string) bool {
	return strings.Contains(mimeType, "html")
}

func (e *HTMLExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	article, err := readability.FromReader(r, nil)
	if err != nil {
		return (&TextExtractor{}).Extract(r, mimeType)
	}

	text := htmlToMarkdown(article.Content)

	return &Result{
		Text:     text,
		Title:    article.Title,
		MimeType: "text/html",
	}, nil
}

func htmlToMarkdown(html string) string {
	md, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		return html
	}
	// Clean up excessive blank lines
	lines := strings.Split(md, "\n")
	var result []string
	blankCount := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankCount++
			if blankCount <= 2 {
				result = append(result, "")
			}
		} else {
			blankCount = 0
			result = append(result, line)
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}
