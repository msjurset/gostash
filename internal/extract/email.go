package extract

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
)

const MIMEEmail = "message/rfc822"

// EmailExtractor extracts text from RFC 2822 email messages (.eml files).
type EmailExtractor struct{}

func (e *EmailExtractor) Supports(mimeType string) bool {
	return mimeType == MIMEEmail || strings.Contains(mimeType, "message/rfc822")
}

func (e *EmailExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, fmt.Errorf("parse email: %w", err)
	}

	subject := decodeHeader(msg.Header.Get("Subject"))
	from := decodeHeader(msg.Header.Get("From"))
	to := decodeHeader(msg.Header.Get("To"))
	date := msg.Header.Get("Date")

	body, err := extractEmailBody(msg)
	if err != nil {
		body = ""
	}

	var text strings.Builder
	if from != "" {
		text.WriteString("From: " + from + "\n")
	}
	if to != "" {
		text.WriteString("To: " + to + "\n")
	}
	if date != "" {
		text.WriteString("Date: " + date + "\n")
	}
	if subject != "" {
		text.WriteString("Subject: " + subject + "\n")
	}
	text.WriteString("\n")
	text.WriteString(body)

	// Normalize Windows line endings
	result := strings.ReplaceAll(strings.TrimSpace(text.String()), "\r\n", "\n")
	result = strings.ReplaceAll(result, "\r", "\n")

	return &Result{
		Text:     result,
		Title:    subject,
		MimeType: MIMEEmail,
		Tags:     []string{"email"},
	}, nil
}

func extractEmailBody(msg *mail.Message) (string, error) {
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}

	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		data, err := io.ReadAll(msg.Body)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return "", fmt.Errorf("multipart message without boundary")
		}
		return extractMultipart(msg.Body, boundary)
	}

	// Single-part message — decode transfer encoding, then transcode to UTF-8
	body, err := decodeBody(msg.Body, msg.Header.Get("Content-Transfer-Encoding"), params["charset"])
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(mediaType, "text/html") {
		return htmlToMarkdown(body), nil
	}
	return coalesceOrphanBullets(body), nil
}

func extractMultipart(r io.Reader, boundary string) (string, error) {
	mr := multipart.NewReader(r, boundary)
	var plain, html string

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		ct := part.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(ct)
		cte := part.Header.Get("Content-Transfer-Encoding")

		// Handle nested multipart (e.g., multipart/alternative inside multipart/mixed)
		if strings.HasPrefix(mediaType, "multipart/") {
			if b := params["boundary"]; b != "" {
				nested, err := extractMultipart(part, b)
				if err == nil && nested != "" {
					if plain == "" {
						plain = nested
					}
				}
				continue
			}
		}

		body, err := decodeBody(part, cte, params["charset"])
		if err != nil {
			continue
		}

		switch {
		case strings.HasPrefix(mediaType, "text/plain"):
			plain = coalesceOrphanBullets(body)
		case strings.HasPrefix(mediaType, "text/html") && plain == "":
			html = htmlToMarkdown(body)
		}
	}

	if plain != "" {
		return plain, nil
	}
	return html, nil
}

// decodeBody reads and decodes a message body according to Content-Transfer-Encoding,
// then transcodes the resulting bytes from `charset` (e.g. "windows-1252") to UTF-8.
// An empty or unknown charset falls back to UTF-8 / pass-through.
func decodeBody(r io.Reader, cte, charset string) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	var decoded []byte
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		// MIME base64 has line breaks — strip them before decoding
		clean := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' {
				return -1
			}
			return r
		}, string(raw))
		d, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			d, err = base64.RawStdEncoding.DecodeString(clean)
			if err != nil {
				d = raw
			}
		}
		decoded = d
	case "quoted-printable":
		d, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(raw))))
		if err != nil {
			d = raw
		}
		decoded = d
	default:
		decoded = raw
	}

	utf8Bytes := transcodeToUTF8(decoded, charset)
	s := strings.ReplaceAll(string(utf8Bytes), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// transcodeToUTF8 converts bytes from the named charset to UTF-8. If the
// charset is empty, unknown, or already UTF-8, the input is returned unchanged
// (with one exception: when the declared charset is UTF-8 but the bytes are
// not valid UTF-8, we leave them alone so callers see the raw input rather
// than a stream of replacement characters).
func transcodeToUTF8(b []byte, charset string) []byte {
	c := strings.ToLower(strings.TrimSpace(charset))
	if c == "" || c == "utf-8" || c == "utf8" || c == "us-ascii" || c == "ascii" {
		return b
	}
	enc, err := htmlindex.Get(c)
	if err != nil || enc == nil || enc == encoding.Nop {
		return b
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return b
	}
	if !utf8.Valid(out) {
		return b
	}
	return out
}

func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}
