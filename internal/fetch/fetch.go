package fetch

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	// Extract readable text from HTML
	if strings.Contains(ct, "html") {
		parsed, _ := url.Parse(rawURL)
		article, err := readability.FromReader(strings.NewReader(string(body)), parsed)
		if err == nil {
			result.Title = article.Title
			result.ExtractedText = article.TextContent
		}
	}

	return result, nil
}
