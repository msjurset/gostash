package fetch

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

// URL fetches a URL, extracts title and readable text.
func URL(rawURL string) (*Result, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(rawURL)
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

	// Extract readable text from HTML
	if strings.Contains(ct, "html") {
		article, err := readability.FromReader(strings.NewReader(string(body)), nil)
		if err == nil {
			result.Title = article.Title
			result.ExtractedText = article.TextContent
		}
	}

	return result, nil
}
