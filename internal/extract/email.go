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

	// Single-part message — decode transfer encoding
	body, err := decodeBody(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(mediaType, "text/html") {
		return htmlToMarkdown(body), nil
	}
	return body, nil
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

		body, err := decodeBody(part, cte)
		if err != nil {
			continue
		}

		switch {
		case strings.HasPrefix(mediaType, "text/plain"):
			plain = body
		case strings.HasPrefix(mediaType, "text/html") && plain == "":
			html = htmlToMarkdown(body)
		}
	}

	if plain != "" {
		return plain, nil
	}
	return html, nil
}

// decodeBody reads and decodes a message body according to Content-Transfer-Encoding.
func decodeBody(r io.Reader, encoding string) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}

	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		// MIME base64 has line breaks — strip them before decoding
		clean := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' {
				return -1
			}
			return r
		}, string(raw))
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(clean)
			if err != nil {
				return string(raw), nil
			}
		}
		// Normalize Windows line endings
		s := strings.ReplaceAll(string(decoded), "\r\n", "\n")
		return strings.ReplaceAll(s, "\r", "\n"), nil
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(raw))))
		if err != nil {
			return string(raw), nil
		}
		return string(data), nil
	default:
		return string(raw), nil
	}
}

func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}
