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
	if res.CapturedAt == nil {
		t.Fatal("CapturedAt should be parsed from the Date header")
	}
	// "Thu, 2 Apr 2026 12:00:00 +0000" → 2026-04-02T12:00:00Z
	if got := res.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-04-02T12:00:00Z" {
		t.Errorf("CapturedAt = %s, want 2026-04-02T12:00:00Z", got)
	}
}

// Received: headers stamp the message later than the Date header
// when the email was relayed / forwarded. mostRecentEmailDate must
// pick the latest stamp so thread-replied messages cluster against
// the thread's most recent activity, not its original send.
func TestEmailExtractor_PrefersMostRecentReceived(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Subject: Reply\r\n" +
		"Date: Mon, 1 Apr 2026 09:00:00 +0000\r\n" +
		"Received: from x by y; Mon, 1 Apr 2026 09:00:30 +0000\r\n" +
		"Received: from y by z; Thu, 4 Apr 2026 17:30:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"reply body\r\n"

	e := &EmailExtractor{}
	res, err := e.Extract(strings.NewReader(raw), MIMEEmail)
	if err != nil {
		t.Fatal(err)
	}
	if res.CapturedAt == nil {
		t.Fatal("CapturedAt should be populated from a Received header")
	}
	if got := res.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-04-04T17:30:00Z" {
		t.Errorf("CapturedAt = %s, want the latest Received timestamp 2026-04-04T17:30:00Z", got)
	}
}

func TestEmailExtractor_Charset(t *testing.T) {
	// Outlook on Windows commonly emits text/plain with charset=windows-1252.
	// Smart quotes (U+2019, "'") encode as a single byte 0x92 in CP1252,
	// which is invalid as UTF-8 and would otherwise render as U+FFFD ("�").
	tests := []struct {
		name    string
		charset string
		body    []byte
		want    string
	}{
		{
			name:    "windows-1252 smart quote",
			charset: "windows-1252",
			body:    []byte{'I', 0x92, 'v', 'e'},
			want:    "I’ve",
		},
		{
			name:    "iso-8859-1 latin chars",
			charset: "iso-8859-1",
			body:    []byte{'c', 'a', 'f', 0xe9}, // café
			want:    "café",
		},
		{
			name:    "utf-8 unchanged",
			charset: "utf-8",
			body:    []byte("I’ve"),
			want:    "I’ve",
		},
		{
			name:    "no charset declared falls through",
			charset: "",
			body:    []byte("plain ascii"),
			want:    "plain ascii",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := "text/plain"
			if tc.charset != "" {
				ct = "text/plain; charset=" + tc.charset
			}
			raw := "From: Alice <alice@example.com>\r\n" +
				"Subject: Encoding test\r\n" +
				"Content-Type: " + ct + "\r\n" +
				"\r\n" +
				string(tc.body)

			e := &EmailExtractor{}
			res, err := e.Extract(strings.NewReader(raw), MIMEEmail)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Text, tc.want) {
				t.Errorf("body missing %q in %q", tc.want, res.Text)
			}
			if strings.Contains(res.Text, "�") {
				t.Errorf("body contains replacement character: %q", res.Text)
			}
		})
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
