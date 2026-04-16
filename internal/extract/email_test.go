package extract

import (
	"strings"
	"testing"
)

func TestEmailExtractor_PlainText(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Subject: Hello\r\n" +
		"Date: Thu, 2 Apr 2026 12:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Line one.\r\nLine two.\r\n"

	e := &EmailExtractor{}
	res, err := e.Extract(strings.NewReader(raw), MIMEEmail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "From: Alice") {
		t.Errorf("missing From header: %q", res.Text)
	}
	if !strings.Contains(res.Text, "\nSubject: Hello\n") {
		t.Errorf("Subject should be on its own line: %q", res.Text)
	}
	if !strings.Contains(res.Text, "Line one.\nLine two.") {
		t.Errorf("body line breaks should be preserved: %q", res.Text)
	}
	if res.Title != "Hello" {
		t.Errorf("title = %q, want %q", res.Title, "Hello")
	}
}

func TestEmailExtractor_HTMLBody_BlockBreaks(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"Subject: HTML body\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body>" +
		"<p>Vini, yes&#8230;</p>" +
		"<p>Erik, no.</p>" +
		"<p>We might keep Erik, but&nbsp;he is not cutting it.</p>" +
		"</body></html>"

	e := &EmailExtractor{}
	res, err := e.Extract(strings.NewReader(raw), MIMEEmail)
	if err != nil {
		t.Fatal(err)
	}
	// Paragraphs must be separated by a blank line so Markdown-aware
	// renderers don't collapse them into a single flowing paragraph.
	if !strings.Contains(res.Text, "Erik, no.") {
		t.Errorf("missing paragraph: %q", res.Text)
	}
	if strings.Contains(res.Text, "no.We") || strings.Contains(res.Text, "yes…Erik") {
		t.Errorf("paragraphs concatenated without break: %q", res.Text)
	}
	// &#8230; should decode to an ellipsis, not be stripped.
	if !strings.Contains(res.Text, "…") {
		t.Errorf("entity should be decoded: %q", res.Text)
	}
}

func TestEmailExtractor_HTMLBody_PreservesLinks(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"Subject: Link test\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		`<p>See <a href="https://example.com/path">the page</a> for details.</p>`

	e := &EmailExtractor{}
	res, err := e.Extract(strings.NewReader(raw), MIMEEmail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "https://example.com/path") {
		t.Errorf("URL not preserved: %q", res.Text)
	}
}

func TestEmailExtractor_Supports(t *testing.T) {
	e := &EmailExtractor{}
	if !e.Supports(MIMEEmail) {
		t.Error("should support message/rfc822")
	}
	if e.Supports("text/plain") {
		t.Error("should not support text/plain")
	}
}
