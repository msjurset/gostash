package fetch

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

	// Extract readable content from HTML and convert to Markdown.
	// We pre-strip structural chrome (nav/header/footer/aside,
	// role=navigation|banner|contentinfo) before handing to readability
	// — without this, sites with poor semantic markup spill their site
	// nav into the "article" and the resulting markdown is mostly
	// menu-link soup.
	if strings.Contains(ct, "html") {
		parsed, _ := url.Parse(rawURL)
		cleaned := stripChrome(body)
		article, err := readability.FromReader(bytes.NewReader(cleaned), parsed)
		if err == nil {
			result.Title = article.Title
			result.ExtractedText = htmlToMarkdown(article.Content)
		}
	}

	return result, nil
}

// chromeTags lists element types that are page chrome, never article
// body. Stripped wholesale before readability runs.
var chromeTags = map[atom.Atom]bool{
	atom.Nav:      true,
	atom.Header:   true,
	atom.Footer:   true,
	atom.Aside:    true,
	atom.Script:   true,
	atom.Style:    true,
	atom.Form:     true,
	atom.Iframe:   true,
	atom.Noscript: true,
}

// chromeRoles lists ARIA roles that mark a region as chrome.
var chromeRoles = map[string]bool{
	"navigation":    true,
	"banner":        true,
	"contentinfo":   true,
	"search":        true,
	"complementary": true,
}

// stripChrome removes page-chrome elements from HTML before readability
// runs, so site nav / footers / sidebars don't get pulled into the
// extracted article body.
func stripChrome(body []byte) []byte {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return body
	}
	removeChromeNodes(doc)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return body
	}
	return buf.Bytes()
}

func removeChromeNodes(n *html.Node) {
	// Walk children with explicit re-link so removing during iteration
	// is safe.
	child := n.FirstChild
	for child != nil {
		next := child.NextSibling
		if isChrome(child) {
			n.RemoveChild(child)
		} else {
			removeChromeNodes(child)
		}
		child = next
	}
}

func isChrome(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if chromeTags[n.DataAtom] {
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "role" && chromeRoles[strings.ToLower(strings.TrimSpace(a.Val))] {
			return true
		}
	}
	// Image-only anchor: <a><img></a> with no meaningful text.
	// On real articles these are virtually always nav / social /
	// icon-link chrome rather than article body.
	if n.DataAtom == atom.A && isImageOnlyLink(n) {
		return true
	}
	return false
}

// isImageOnlyLink reports whether an <a> element contains exclusively
// images (and whitespace) — the canonical icon-link nav pattern.
func isImageOnlyLink(a *html.Node) bool {
	hasImg := false
	for c := a.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if strings.TrimSpace(c.Data) != "" {
				return false
			}
		case html.ElementNode:
			if c.DataAtom == atom.Img {
				hasImg = true
				continue
			}
			// Recurse into wrappers (span, div, etc.) — bail if any
			// real text turns up underneath.
			if !isImageOnlyContainer(c) {
				return false
			}
		}
	}
	return hasImg
}

func isImageOnlyContainer(n *html.Node) bool {
	hasContent := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if strings.TrimSpace(c.Data) != "" {
				return false
			}
		case html.ElementNode:
			if c.DataAtom == atom.Img {
				hasContent = true
				continue
			}
			if !isImageOnlyContainer(c) {
				return false
			}
			hasContent = true
		}
	}
	_ = hasContent
	return true
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
