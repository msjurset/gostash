package extract

import (
	"strings"
	"testing"
)

func TestExtractThumbnailCandidates_OGImageWins(t *testing.T) {
	html := `<html><head>
		<meta property="og:image" content="https://example.com/hero.jpg">
		<meta name="twitter:image" content="https://example.com/twitter.jpg">
		<link rel="apple-touch-icon" sizes="180x180" href="/icon.png">
	</head><body>
		<img src="/banner.jpg" width="800" height="400">
	</body></html>`

	cands, err := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/article")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) < 4 {
		t.Fatalf("expected ≥4 candidates, got %d: %+v", len(cands), cands)
	}
	if cands[0].Source != "og" || cands[0].URL != "https://example.com/hero.jpg" {
		t.Errorf("top candidate should be og:image, got %+v", cands[0])
	}
	if cands[0].Score <= cands[1].Score {
		t.Errorf("og should outrank others; top=%d second=%d", cands[0].Score, cands[1].Score)
	}
}

func TestExtractThumbnailCandidates_RelativeURLsResolved(t *testing.T) {
	html := `<html><head>
		<meta property="og:image" content="/img/foo.jpg">
	</head></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/blog/post")
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	if cands[0].URL != "https://example.com/img/foo.jpg" {
		t.Errorf("relative URL not resolved: %s", cands[0].URL)
	}
}

func TestExtractThumbnailCandidates_DedupeKeepsBestSource(t *testing.T) {
	// og:image and twitter:image both point at the same file.
	// Dedupe should keep one entry with the higher (og) score.
	html := `<html><head>
		<meta property="og:image" content="https://example.com/same.jpg">
		<meta name="twitter:image" content="https://example.com/same.jpg">
	</head></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/")
	if len(cands) != 1 {
		t.Fatalf("expected dedup to 1, got %d", len(cands))
	}
	if cands[0].Source != "og" {
		t.Errorf("dedup should retain og source, got %s", cands[0].Source)
	}
}

func TestExtractThumbnailCandidates_JSONLDImage(t *testing.T) {
	html := `<html><head>
		<script type="application/ld+json">
		{"@context":"https://schema.org","@type":"Article",
		 "image":{"url":"https://cdn.example.com/img.jpg","width":1200,"height":630}}
		</script>
	</head></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/")
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].URL != "https://cdn.example.com/img.jpg" {
		t.Errorf("got URL %s", cands[0].URL)
	}
	if cands[0].Width != 1200 || cands[0].Height != 630 {
		t.Errorf("dims %dx%d, want 1200x630", cands[0].Width, cands[0].Height)
	}
}

func TestExtractThumbnailCandidates_InPageImageBelowFloorSkipped(t *testing.T) {
	// 100x100 image with explicit dims should be filtered out.
	html := `<html><body>
		<img src="/logo.png" width="100" height="100">
	</body></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/")
	if len(cands) != 0 {
		t.Errorf("small img should be filtered, got %+v", cands)
	}
}

func TestExtractThumbnailCandidates_DataURISkipped(t *testing.T) {
	// data: URLs are not actionable thumbnails — they'd inflate the
	// candidate set without giving the user anything to download.
	html := `<html><head>
		<meta property="og:image" content="data:image/png;base64,iVBOR...">
	</head></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/")
	if len(cands) != 0 {
		t.Errorf("data: URI should be filtered, got %+v", cands)
	}
}

func TestExtractThumbnailCandidates_SrcsetLargestPicked(t *testing.T) {
	html := `<html><body>
		<img srcset="/small.jpg 200w, /large.jpg 1200w" width="600" height="400">
	</body></html>`
	cands, _ := ExtractThumbnailCandidates(strings.NewReader(html), "https://example.com/")
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(cands), cands)
	}
	if !strings.HasSuffix(cands[0].URL, "/large.jpg") {
		t.Errorf("expected largest srcset entry, got %s", cands[0].URL)
	}
}
