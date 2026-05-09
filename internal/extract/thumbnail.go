package extract

import (
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ThumbnailCandidate is one image option found during HTML extraction.
// The same image URL can appear multiple times under different sources
// (e.g. og:image and twitter:image often point at the same file);
// `RankCandidates` dedupes by absolute URL, keeping the highest-scoring
// origin.
type ThumbnailCandidate struct {
	URL    string `json:"url"`
	Source string `json:"source"`           // og, twitter, schema, apple-touch, in-page
	Width  int    `json:"width,omitempty"`  // px, when known
	Height int    `json:"height,omitempty"` // px, when known
	Score  int    `json:"score"`
}

// Score weights per source. Higher = more authoritative. og:image
// almost always points at the editor-chosen hero image; in-page <img>
// is the noisy fallback only used when nothing else surfaces.
const (
	scoreOG          = 1000
	scoreTwitter     = 900
	scoreSchema      = 800
	scoreAppleTouch  = 600
	scoreInPage      = 100
	minInPageDim     = 200 // px; smaller in-page imgs are almost always logos/icons
	maxInPageResults = 5   // cap so a 200-img product page doesn't flood the list
)

// ExtractThumbnailCandidates parses an HTML body and returns a ranked
// list of candidate thumbnail images (best first). `pageURL` is used
// to resolve relative paths to absolute URLs. Returns an empty slice
// when nothing usable is present.
func ExtractThumbnailCandidates(r io.Reader, pageURL string) ([]ThumbnailCandidate, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(pageURL)

	var cands []ThumbnailCandidate
	inPageCount := 0
	var visit func(n *html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				if c := metaCandidate(n, base); c != nil {
					cands = append(cands, *c)
				}
			case "link":
				if c := linkCandidate(n, base); c != nil {
					cands = append(cands, *c)
				}
			case "img":
				if inPageCount < maxInPageResults {
					if c := imgCandidate(n, base); c != nil {
						cands = append(cands, *c)
						inPageCount++
					}
				}
			case "script":
				if attr(n, "type") == "application/ld+json" && n.FirstChild != nil {
					for _, c := range jsonLDCandidates(n.FirstChild.Data, base) {
						cands = append(cands, c)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(doc)
	return RankCandidates(cands), nil
}

// RankCandidates dedupes by URL (keeping the highest-scoring source
// for any image that appears under multiple meta tags) and returns
// the list sorted best-first. Exposed so callers receiving a
// pre-built list (e.g. tests) can normalize it.
func RankCandidates(in []ThumbnailCandidate) []ThumbnailCandidate {
	byURL := map[string]ThumbnailCandidate{}
	for _, c := range in {
		if c.URL == "" {
			continue
		}
		if existing, ok := byURL[c.URL]; ok {
			if c.Score > existing.Score {
				byURL[c.URL] = c
			}
			continue
		}
		byURL[c.URL] = c
	}
	out := make([]ThumbnailCandidate, 0, len(byURL))
	for _, c := range byURL {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

// --- helpers ---

func metaCandidate(n *html.Node, base *url.URL) *ThumbnailCandidate {
	name := strings.ToLower(attr(n, "name"))
	prop := strings.ToLower(attr(n, "property"))
	content := strings.TrimSpace(attr(n, "content"))
	if content == "" {
		return nil
	}
	var source string
	var score int
	switch {
	case prop == "og:image", prop == "og:image:url", prop == "og:image:secure_url":
		source = "og"
		score = scoreOG
	case name == "twitter:image", name == "twitter:image:src", prop == "twitter:image":
		source = "twitter"
		score = scoreTwitter
	default:
		return nil
	}
	abs := absURL(content, base)
	if abs == "" {
		return nil
	}
	return &ThumbnailCandidate{URL: abs, Source: source, Score: score}
}

func linkCandidate(n *html.Node, base *url.URL) *ThumbnailCandidate {
	rel := strings.ToLower(attr(n, "rel"))
	if !strings.Contains(rel, "apple-touch-icon") {
		return nil
	}
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" {
		return nil
	}
	abs := absURL(href, base)
	if abs == "" {
		return nil
	}
	w, h := parseSizes(attr(n, "sizes"))
	// Apple-touch-icon scoring includes a small bonus for declared
	// size so a 180×180 wins over a 57×57 when both are present.
	bonus := 0
	if w > 0 {
		bonus = w / 60 // ~3 for 180px, ~1 for 57px
	}
	return &ThumbnailCandidate{
		URL:    abs,
		Source: "apple-touch",
		Width:  w,
		Height: h,
		Score:  scoreAppleTouch + bonus,
	}
}

func imgCandidate(n *html.Node, base *url.URL) *ThumbnailCandidate {
	src := strings.TrimSpace(attr(n, "src"))
	if src == "" {
		// Pull from srcset's largest entry if present.
		if ss := attr(n, "srcset"); ss != "" {
			src = pickLargestFromSrcset(ss)
		}
	}
	if src == "" {
		return nil
	}
	w, _ := strconv.Atoi(attr(n, "width"))
	h, _ := strconv.Atoi(attr(n, "height"))
	// Quality floor: skip unless we have *some* signal the image is
	// meaningful (declared dim ≥ 200 OR it's the only thing we can
	// see). To stay honest we require declared dims; tiny logos and
	// trackers without dims slip through untouched, but the scoring
	// keeps them below every meta-derived candidate.
	if w > 0 && (w < minInPageDim || h < minInPageDim) {
		return nil
	}
	abs := absURL(src, base)
	if abs == "" {
		return nil
	}
	bonus := 0
	if w > 0 {
		bonus = w / 100 // up to ~10 for 1000px wide images
	}
	return &ThumbnailCandidate{
		URL:    abs,
		Source: "in-page",
		Width:  w,
		Height: h,
		Score:  scoreInPage + bonus,
	}
}

// jsonLDCandidates extracts `image` fields from a JSON-LD blob.
// Schema.org permits `image` to be a string, an `ImageObject`
// `{url, width, height}`, or an array of either. We walk the parsed
// JSON looking for any `image` key and accept whichever shape we hit.
func jsonLDCandidates(raw string, base *url.URL) []ThumbnailCandidate {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	var out []ThumbnailCandidate
	walkJSON(v, &out, base)
	return out
}

func walkJSON(v any, out *[]ThumbnailCandidate, base *url.URL) {
	switch t := v.(type) {
	case map[string]any:
		if img, ok := t["image"]; ok {
			collectJSONImage(img, out, base)
		}
		for _, child := range t {
			walkJSON(child, out, base)
		}
	case []any:
		for _, child := range t {
			walkJSON(child, out, base)
		}
	}
}

func collectJSONImage(v any, out *[]ThumbnailCandidate, base *url.URL) {
	switch t := v.(type) {
	case string:
		abs := absURL(t, base)
		if abs != "" {
			*out = append(*out, ThumbnailCandidate{URL: abs, Source: "schema", Score: scoreSchema})
		}
	case map[string]any:
		if u, ok := t["url"].(string); ok {
			abs := absURL(u, base)
			if abs != "" {
				w, _ := numAsInt(t["width"])
				h, _ := numAsInt(t["height"])
				*out = append(*out, ThumbnailCandidate{
					URL: abs, Source: "schema", Width: w, Height: h,
					Score: scoreSchema,
				})
			}
		}
	case []any:
		for _, child := range t {
			collectJSONImage(child, out, base)
		}
	}
}

func numAsInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	}
	return 0, false
}

// pickLargestFromSrcset returns the URL with the largest width
// descriptor in a srcset attribute. Falls back to the first URL when
// no widths are present.
func pickLargestFromSrcset(ss string) string {
	parts := strings.Split(ss, ",")
	bestURL := ""
	bestW := -1
	for _, p := range parts {
		fields := strings.Fields(strings.TrimSpace(p))
		if len(fields) == 0 {
			continue
		}
		u := fields[0]
		w := 0
		if len(fields) > 1 {
			d := fields[1]
			if strings.HasSuffix(d, "w") {
				w, _ = strconv.Atoi(strings.TrimSuffix(d, "w"))
			}
		}
		if bestURL == "" || w > bestW {
			bestURL = u
			bestW = w
		}
	}
	return bestURL
}

// parseSizes parses an HTML `sizes` attribute like "180x180" or "any"
// (the latter common on apple-touch-icon links) and returns
// (width, height). Returns (0, 0) for unparseable or `any`.
func parseSizes(raw string) (int, int) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "any" {
		return 0, 0
	}
	// `sizes` may list multiple, space-separated. Pick the largest.
	bestW := 0
	bestH := 0
	for _, part := range strings.Fields(raw) {
		x := strings.IndexByte(part, 'x')
		if x <= 0 {
			continue
		}
		w, _ := strconv.Atoi(part[:x])
		h, _ := strconv.Atoi(part[x+1:])
		if w > bestW {
			bestW = w
			bestH = h
		}
	}
	return bestW, bestH
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func absURL(raw string, base *url.URL) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		// Reject data:, javascript:, etc.
		return ""
	}
	if isSpacerURL(u.Path) {
		return ""
	}
	return u.String()
}

// spacerPatterns lists URL-path substrings that virtually always
// identify tracking pixels, layout spacers, or 1×1 transparent GIFs.
// Amazon, e-commerce CDNs, and analytics scripts emit these
// constantly and they pass meta-tag and content-type filters cleanly,
// so the only place to catch them cheaply is in the URL itself.
var spacerPatterns = []string{
	"transparent-pixel",
	"transparent_pixel",
	"transparent.gif",
	"transparent.png",
	"spacer.gif",
	"spacer.png",
	"blank.gif",
	"blank.png",
	"pixel.gif",
	"pixel.png",
	"1x1.gif",
	"1x1.png",
	"tracking-pixel",
	"clear.gif",
	"clear.png",
}

func isSpacerURL(path string) bool {
	p := strings.ToLower(path)
	for _, pat := range spacerPatterns {
		if strings.Contains(p, pat) {
			return true
		}
	}
	return false
}
