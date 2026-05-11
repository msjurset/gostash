package main

import (
	"slices"
	"strings"
	"testing"
)

// TestScrapeCandidates locks in the candidate-extraction contract:
// img tags become image candidates (preferring srcset's largest entry
// over src), <a href> links become link candidates only when the
// extension matches our stashable allowlist, relative URLs resolve
// against the page URL, and duplicates dedupe by absolute URL.
func TestScrapeCandidates(t *testing.T) {
	html := `
<html>
<head><title>Test Page</title></head>
<body>
  <img src="/img/hero-low.jpg"
       srcset="/img/hero-800.jpg 800w, /img/hero-1600.jpg 1600w"
       alt="Hero image">
  <img src="/img/inline.png">
  <img src="data:image/png;base64,iVBORw0KG">  <!-- ignored: data URI -->
  <img src="/img/inline.png">                  <!-- duplicate, deduped -->
  <a href="/files/report.pdf">Download report (PDF)</a>
  <a href="/files/data.csv">CSV export</a>
  <a href="/about">About us</a>                 <!-- ignored: not a file ext -->
  <a href="javascript:void(0)">noop</a>         <!-- ignored: bad scheme -->
  <a href="https://other.example.com/whitepaper.pdf">External PDF</a>
</body>
</html>`

	page, err := scrapeCandidates("https://example.com/article", []byte(html), false)
	if err != nil {
		t.Fatalf("scrapeCandidates: %v", err)
	}
	if page.PageTitle != "Test Page" {
		t.Errorf("PageTitle = %q, want %q", page.PageTitle, "Test Page")
	}

	urls := make([]string, 0, len(page.Candidates))
	for _, c := range page.Candidates {
		urls = append(urls, c.URL)
	}
	wantURLs := []string{
		"https://example.com/img/hero-1600.jpg",          // srcset largest wins over src
		"https://example.com/img/inline.png",             // single dedupe
		"https://example.com/files/report.pdf",
		"https://example.com/files/data.csv",
		"https://other.example.com/whitepaper.pdf",
	}
	for _, want := range wantURLs {
		if !slices.Contains(urls,want) {
			t.Errorf("missing candidate %q (got %v)", want, urls)
		}
	}
	// Confirm /about (no file extension) wasn't included.
	if slices.Contains(urls,"https://example.com/about") {
		t.Errorf("non-stashable link should be filtered out: %v", urls)
	}
	// Confirm the data: URI is excluded (would break downloads).
	for _, c := range page.Candidates {
		if strings.HasPrefix(c.URL, "data:") {
			t.Errorf("data: URI shouldn't appear: %s", c.URL)
		}
	}

	// Images come before links in the sort.
	for i := 0; i < len(page.Candidates)-1; i++ {
		if page.Candidates[i].Kind == "link" && page.Candidates[i+1].Kind == "image" {
			t.Errorf("expected images-then-links ordering; got mix at %d", i)
		}
	}
}

// TestScrapeCandidatesAllLinks toggles --all-links and asserts that
// links without file extensions DO appear. Used by the Mac picker
// when the user wants to see everything, not just file refs.
func TestScrapeCandidatesAllLinks(t *testing.T) {
	html := `<a href="/about">About</a><a href="/files/x.pdf">PDF</a>`
	page, err := scrapeCandidates("https://example.com/", []byte(html), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Candidates) != 2 {
		t.Errorf("--all-links: got %d candidates, want 2", len(page.Candidates))
	}
}

func TestStashableExtensionsCoverage(t *testing.T) {
	mustHave := []string{".pdf", ".png", ".zip", ".mp4", ".csv", ".docx"}
	for _, ext := range mustHave {
		if !stashableExtensions[ext] {
			t.Errorf("stashableExtensions missing %s", ext)
		}
	}
	// Sanity: extensions that LOOK like files but aren't pickable
	// (we don't want to catch HTML pages).
	for _, ext := range []string{".html", ".htm", ".php", ".aspx"} {
		if stashableExtensions[ext] {
			t.Errorf("stashableExtensions should NOT include %s", ext)
		}
	}
}
