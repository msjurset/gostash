package feeds

import (
	"strings"
	"testing"
)

func TestParseRSS2(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Blog</title>
    <item>
      <title>Hello world</title>
      <link>https://example.com/post-1</link>
      <guid isPermaLink="true">https://example.com/post-1</guid>
      <description>First post.</description>
      <pubDate>Mon, 12 May 2026 10:30:00 +0000</pubDate>
      <enclosure url="https://example.com/img.jpg" type="image/jpeg" length="12345"/>
    </item>
    <item>
      <title>Second post</title>
      <link>https://example.com/post-2</link>
      <guid>post-2-guid</guid>
    </item>
  </channel>
</rss>`)
	items, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Title != "Hello world" {
		t.Errorf("title: %q", items[0].Title)
	}
	if items[0].URL != "https://example.com/post-1" {
		t.Errorf("url: %q", items[0].URL)
	}
	if items[0].GUID != "https://example.com/post-1" {
		t.Errorf("guid: %q", items[0].GUID)
	}
	if items[0].ThumbnailURL != "https://example.com/img.jpg" {
		t.Errorf("thumb: %q", items[0].ThumbnailURL)
	}
	if items[0].PublishedAt == nil {
		t.Error("publishedAt nil")
	}
	if items[1].GUID != "post-2-guid" {
		t.Errorf("guid 2: %q", items[1].GUID)
	}
}

func TestParseAtom(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atomic Channel</title>
  <entry>
    <id>urn:uuid:abc-123</id>
    <title>Atom post</title>
    <link rel="alternate" href="https://example.com/atom-1"/>
    <summary>Hi from Atom.</summary>
    <published>2026-05-12T10:30:00Z</published>
  </entry>
</feed>`)
	items, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].GUID != "urn:uuid:abc-123" {
		t.Errorf("guid: %q", items[0].GUID)
	}
	if items[0].URL != "https://example.com/atom-1" {
		t.Errorf("url: %q", items[0].URL)
	}
	if items[0].PublishedAt == nil {
		t.Error("publishedAt nil")
	}
}

func TestParseRSS1(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
         xmlns="http://purl.org/rss/1.0/"
         xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel rdf:about="https://example.com/rdf"/>
  <item rdf:about="https://example.com/rdf/1">
    <title>RDF entry</title>
    <link>https://example.com/rdf/1</link>
    <description>RDF body</description>
    <dc:date>2026-05-11</dc:date>
  </item>
</rdf:RDF>`)
	items, err := Parse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if !strings.HasPrefix(items[0].GUID, "https://example.com/rdf/1") {
		t.Errorf("guid: %q", items[0].GUID)
	}
}

func TestParseUnknownRoot(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><html><body>no feed here</body></html>`)
	_, err := Parse(body)
	if err == nil {
		t.Fatal("expected error for non-feed XML, got nil")
	}
}
