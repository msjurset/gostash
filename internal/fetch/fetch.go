package fetch

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
)

// Result holds fetched page data.
type Result struct {
	Title         string
	URL           string
	Body          []byte // raw HTML
	ExtractedText string
	MimeType      string
}

// URL fetches a URL, extracts title and readable text as Markdown.
func URL(rawURL string) (*Result, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	// Many sites serve server-rendered HTML to recognized browsers/bots
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	result := &Result{
		URL:      rawURL,
		Body:     body,
		MimeType: ct,
	}

	// Extract readable content from HTML and convert to Markdown
	if strings.Contains(ct, "html") {
		parsed, _ := url.Parse(rawURL)
		article, err := readability.FromReader(strings.NewReader(string(body)), parsed)
		if err == nil {
			result.Title = article.Title
			result.ExtractedText = htmlToMarkdown(article.Content)
		}
	}

	return result, nil
}

// listHeadingRe matches list markers before markdown headings: "- ## Heading" → "## Heading"
var listHeadingRe = regexp.MustCompile(`^\s*[-*]\s+(#{1,6}\s)`)

// htmlToMarkdown converts cleaned HTML to Markdown.
func htmlToMarkdown(html string) string {
	md, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		// Fall back to stripping tags
		return stripHTMLTags(html)
	}
	// Clean up converter artifacts
	lines := strings.Split(md, "\n")
	var result []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount <= 2 {
				result = append(result, "")
			}
		} else {
			blankCount = 0
			// Fix headings wrapped in list items: "- ## Heading" → "## Heading"
			line = listHeadingRe.ReplaceAllString(line, "$1")
			result = append(result, line)
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}
