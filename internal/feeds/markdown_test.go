package feeds

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdownPassThroughForPlainText(t *testing.T) {
	in := "Just plain text, no HTML here."
	if got := HTMLToMarkdown(in); got != in {
		t.Errorf("plain text mutated: %q -> %q", in, got)
	}
}

func TestHTMLToMarkdownParagraphsAndLinks(t *testing.T) {
	in := `<p>Hello <a href="https://example.com">world</a>.</p><p>Second graf.</p>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "[world](https://example.com)") {
		t.Errorf("link not converted: %q", got)
	}
	if !strings.Contains(got, "Second graf.") {
		t.Errorf("second paragraph missing: %q", got)
	}
	// Two paragraphs should be blank-line separated.
	if !strings.Contains(got, "world](https://example.com).\n\nSecond graf.") {
		t.Errorf("paragraph separation off: %q", got)
	}
}

func TestHTMLToMarkdownEmphasisAndCode(t *testing.T) {
	in := `<p><strong>bold</strong> and <em>italic</em> with <code>code</code> inline.</p>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "**bold**") {
		t.Errorf("bold missing: %q", got)
	}
	if !strings.Contains(got, "*italic*") {
		t.Errorf("italic missing: %q", got)
	}
	if !strings.Contains(got, "`code`") {
		t.Errorf("code missing: %q", got)
	}
}

func TestHTMLToMarkdownBlockquote(t *testing.T) {
	in := `<blockquote><p>quoted line one.</p><p>line two.</p></blockquote>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "> quoted line one.") {
		t.Errorf("blockquote line one missing prefix: %q", got)
	}
	if !strings.Contains(got, "> line two.") {
		t.Errorf("blockquote line two missing prefix: %q", got)
	}
}

func TestHTMLToMarkdownLists(t *testing.T) {
	in := `<ul><li>one</li><li>two</li></ul>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "- one") || !strings.Contains(got, "- two") {
		t.Errorf("ul not converted: %q", got)
	}
	in = `<ol><li>first</li><li>second</li></ol>`
	got = HTMLToMarkdown(in)
	if !strings.Contains(got, "1. first") || !strings.Contains(got, "2. second") {
		t.Errorf("ol not converted: %q", got)
	}
}

func TestHTMLToMarkdownHeadings(t *testing.T) {
	in := `<h1>Top</h1><h3>Sub</h3>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "# Top") {
		t.Errorf("h1 missing: %q", got)
	}
	if !strings.Contains(got, "### Sub") {
		t.Errorf("h3 missing: %q", got)
	}
}

func TestHTMLToMarkdownDaringFireballStyle(t *testing.T) {
	// Real-ish RSS HTML the user just hit on item 01KRF9YKE8XY.
	in := `<p>Kagi&rsquo;s documentation:</p>` +
		`<blockquote><p>Typing <code>@r headphones</code> will search for &ldquo;headphones&rdquo; but limit the` +
		` results to reddit.com (<code>r</code> is the short code for Reddit).</p></blockquote>` +
		`<p>I&rsquo;ve never actually looked any of these up.</p>`
	got := HTMLToMarkdown(in)
	if !strings.Contains(got, "Kagi") {
		t.Fatalf("missing first line: %q", got)
	}
	if !strings.Contains(got, "> Typing `@r headphones`") {
		t.Errorf("blockquote-with-inline-code missing: %q", got)
	}
	if !strings.Contains(got, "I") && strings.Contains(got, "actually looked") {
		t.Errorf("third paragraph missing: %q", got)
	}
}
