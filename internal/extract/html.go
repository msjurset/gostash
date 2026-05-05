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
	md = coalesceOrphanBullets(md)
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

// coalesceOrphanBullets fixes a malformed-list pattern that some HTML→MD
// converters emit when list items contain block-level children
// (`<li><div>…</div></li>`): the bullet marker lands on its own line and the
// content falls onto the next line, with one or more blank lines between
// them. SwiftUI's Markdown renderer then sees a bare "*" and a separate
// paragraph instead of a list item. Rejoin them onto a single line so the
// renderer treats the construct as a real bullet.
func coalesceOrphanBullets(md string) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if isOrphanBullet(trimmed) {
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j < len(lines) {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				out = append(out, indent+trimmed+" "+strings.TrimSpace(lines[j]))
				i = j + 1
				continue
			}
		}
		out = append(out, line)
		i++
	}
	return strings.Join(out, "\n")
}

func isOrphanBullet(s string) bool {
	switch s {
	case "*", "-", "+":
		return true
	}
	return false
}
