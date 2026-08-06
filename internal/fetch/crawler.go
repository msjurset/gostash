package fetch

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// CrawlRelatedContext fetches the root URL, finds "about", "product", or "pricing" links,
// fetches up to 3 of them, and returns their concatenated extracted text.
func CrawlRelatedContext(rawURL string) (string, error) {
	rootRes, err := URL(rawURL)
	if err != nil {
		return "", err
	}

	parsedRoot, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	doc, err := html.Parse(bytes.NewReader(rootRes.Body))
	if err != nil {
		return "", err
	}

	var links []string
	seen := make(map[string]bool)
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					u, err := parsedRoot.Parse(a.Val)
					if err == nil && u.Host == parsedRoot.Host {
						p := strings.ToLower(u.Path)
						if strings.Contains(p, "about") || strings.Contains(p, "product") || strings.Contains(p, "pricing") || strings.Contains(p, "mission") || strings.Contains(p, "feature") || strings.Contains(p, "team") {
							cleanURL := u.Scheme + "://" + u.Host + u.Path
							if !seen[cleanURL] && cleanURL != rawURL {
								seen[cleanURL] = true
								links = append(links, cleanURL)
							}
						}
					}
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	if len(links) > 3 {
		links = links[:3]
	}

	var b strings.Builder
	for _, l := range links {
		res, err := URL(l)
		if err == nil && res.ExtractedText != "" {
			b.WriteString(fmt.Sprintf("\n\n--- Page: %s ---\n%s", l, res.ExtractedText))
		}
	}

	return b.String(), nil
}
