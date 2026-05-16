// Package feeds parses syndication feeds (RSS 2.0, Atom 1.0, RDF) and
// returns a normalized FeedItem list. It uses only the standard library
// — no third-party dep — because the subset of feed shapes we care
// about is small and stable, and adding gofeed for one parser pass
// would pull a transitive surface we don't need elsewhere in gostash.
package feeds

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FeedItem is one normalized entry across RSS/Atom/RDF shapes.
type FeedItem struct {
	GUID         string     // RSS <guid>, Atom <id>, falls back to URL
	URL          string     // RSS <link>, Atom <link rel="alternate" href=...>
	Title        string
	Description  string     // plain or HTML; consumers should treat as untrusted
	ThumbnailURL string     // RSS <enclosure>, <media:thumbnail>, Atom <link rel="enclosure">
	PublishedAt  *time.Time // pubDate / published / updated
}

// Fetch downloads and parses a feed from rawURL. The user-agent
// identifies us politely; some servers 403 unidentified curl-like
// clients. Times out at 30s; well-behaved feeds respond in <2s.
func Fetch(rawURL string) ([]FeedItem, error) {
	if rawURL == "" {
		return nil, errors.New("empty feed URL")
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "gostash-feed-poller/1.0 (+https://github.com/msjurset/gostash)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// 10MB cap. A well-formed RSS feed of 50 entries is <500KB;
	// anything bigger is suspicious enough to refuse.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Parse a feed body and return its entries. Auto-detects RSS 2.0,
// Atom 1.0, and RDF (RSS 1.0) by probing the root element. Returns
// items in feed order (newest first by convention; we don't re-sort).
func Parse(body []byte) ([]FeedItem, error) {
	// Probe for root element to pick the right shape. Inexpensive
	// because we stop on the first StartElement.
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	dec.CharsetReader = identityCharsetReader
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("scan root: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "rss":
			return parseRSS2(body)
		case "feed":
			return parseAtom(body)
		case "RDF":
			return parseRSS1(body)
		default:
			return nil, fmt.Errorf("unknown feed root: %q", se.Name.Local)
		}
	}
}

// identityCharsetReader lets the parser accept non-UTF8 declarations
// (e.g. windows-1252) without aborting. We treat the bytes as-is —
// the parser is tolerant enough that mismatched charsets affect only
// non-ASCII characters which we either pass through to the candidate
// title/description or filter at render time.
func identityCharsetReader(_ string, input io.Reader) (io.Reader, error) {
	return input, nil
}

// ────────────────────────────────────
// RSS 2.0
// ────────────────────────────────────

type rss2Feed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []rss2Item `xml:"item"`
	} `xml:"channel"`
}

type rss2Item struct {
	Title          string         `xml:"title"`
	Link           string         `xml:"link"`
	GUID           rss2GUID       `xml:"guid"`
	Description    string         `xml:"description"`
	// ContentEncoded is `<content:encoded>` in the content module
	// namespace — the canonical place for the full HTML body in
	// RSS 2.0 feeds that publish more than an abstract. We prefer
	// it over `<description>` when both are present so the Inbox
	// preview gets real content.
	ContentEncoded string         `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate        string         `xml:"pubDate"`
	Enclosure      rss2Enclosure  `xml:"enclosure"`
	MediaThumb     rss2MediaThumb `xml:"http://search.yahoo.com/mrss/ thumbnail"`
}

type rss2GUID struct {
	Value     string `xml:",chardata"`
	IsPermaLink string `xml:"isPermaLink,attr"`
}

type rss2Enclosure struct {
	URL  string `xml:"url,attr"`
	Type string `xml:"type,attr"`
}

type rss2MediaThumb struct {
	URL string `xml:"url,attr"`
}

func parseRSS2(body []byte) ([]FeedItem, error) {
	var f rss2Feed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	dec.CharsetReader = identityCharsetReader
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode rss2: %w", err)
	}
	out := make([]FeedItem, 0, len(f.Channel.Items))
	for _, it := range f.Channel.Items {
		// Prefer the full body in `<content:encoded>` over the often-
		// truncated `<description>`. Falls back to `<description>` so
		// feeds that only populate the short form still render
		// something.
		desc := strings.TrimSpace(it.ContentEncoded)
		if desc == "" {
			desc = strings.TrimSpace(it.Description)
		}
		fi := FeedItem{
			Title:       strings.TrimSpace(it.Title),
			URL:         strings.TrimSpace(it.Link),
			Description: desc,
			GUID:        strings.TrimSpace(it.GUID.Value),
		}
		if fi.GUID == "" {
			fi.GUID = fi.URL
		}
		if it.Enclosure.URL != "" && strings.HasPrefix(it.Enclosure.Type, "image/") {
			fi.ThumbnailURL = it.Enclosure.URL
		}
		if fi.ThumbnailURL == "" && it.MediaThumb.URL != "" {
			fi.ThumbnailURL = it.MediaThumb.URL
		}
		if t := parseFeedTime(it.PubDate); t != nil {
			fi.PublishedAt = t
		}
		if fi.URL == "" && fi.GUID == "" {
			continue // can't dedup or open it
		}
		out = append(out, fi)
	}
	return out, nil
}

// ────────────────────────────────────
// Atom 1.0
// ────────────────────────────────────

type atomFeed struct {
	XMLName xml.Name   `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

func parseAtom(body []byte) ([]FeedItem, error) {
	var f atomFeed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	dec.CharsetReader = identityCharsetReader
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode atom: %w", err)
	}
	out := make([]FeedItem, 0, len(f.Entries))
	for _, e := range f.Entries {
		fi := FeedItem{
			GUID:  strings.TrimSpace(e.ID),
			Title: strings.TrimSpace(e.Title),
		}
		// Prefer full content; fall back to summary when the feed
		// only ships an abstract. Most blogs publish full content;
		// the inbox preview pane has room to render it.
		fi.Description = strings.TrimSpace(e.Content)
		if fi.Description == "" {
			fi.Description = strings.TrimSpace(e.Summary)
		}
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				fi.URL = strings.TrimSpace(l.Href)
				break
			}
		}
		for _, l := range e.Links {
			if l.Rel == "enclosure" && strings.HasPrefix(l.Type, "image/") {
				fi.ThumbnailURL = l.Href
				break
			}
		}
		// Atom <published> is the original publication; <updated> is
		// the latest edit. Use published when available so candidate
		// ordering matches user expectation.
		if t := parseFeedTime(e.Published); t != nil {
			fi.PublishedAt = t
		} else if t := parseFeedTime(e.Updated); t != nil {
			fi.PublishedAt = t
		}
		if fi.GUID == "" {
			fi.GUID = fi.URL
		}
		if fi.URL == "" && fi.GUID == "" {
			continue
		}
		out = append(out, fi)
	}
	return out, nil
}

// ────────────────────────────────────
// RDF (RSS 1.0)
// ────────────────────────────────────

type rdfFeed struct {
	XMLName xml.Name `xml:"RDF"`
	Items   []rdfItem `xml:"item"`
}

type rdfItem struct {
	About       string `xml:"about,attr"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Date        string `xml:"date"` // dc:date
}

func parseRSS1(body []byte) ([]FeedItem, error) {
	var f rdfFeed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	dec.CharsetReader = identityCharsetReader
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode rdf: %w", err)
	}
	out := make([]FeedItem, 0, len(f.Items))
	for _, it := range f.Items {
		fi := FeedItem{
			GUID:        strings.TrimSpace(it.About),
			URL:         strings.TrimSpace(it.Link),
			Title:       strings.TrimSpace(it.Title),
			Description: strings.TrimSpace(it.Description),
		}
		if fi.GUID == "" {
			fi.GUID = fi.URL
		}
		if t := parseFeedTime(it.Date); t != nil {
			fi.PublishedAt = t
		}
		if fi.URL == "" && fi.GUID == "" {
			continue
		}
		out = append(out, fi)
	}
	return out, nil
}

// parseFeedTime tries a handful of common feed-date formats and
// returns nil on failure (callers fall back to discovered_at).
func parseFeedTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"Mon, 02 Jan 2006 15:04:05 MST",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
