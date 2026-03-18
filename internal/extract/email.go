package extract

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
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

	return &Result{
		Text:     strings.TrimSpace(text.String()),
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
		// Fall back to reading body as plain text
		data, err := io.ReadAll(msg.Body)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	if strings.HasPrefix(mediaType, "text/plain") {
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

	// Single-part HTML or other — read as-is
	data, err := io.ReadAll(msg.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
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
		mediaType, _, _ := mime.ParseMediaType(ct)

		data, err := io.ReadAll(part)
		if err != nil {
			continue
		}

		switch {
		case strings.HasPrefix(mediaType, "text/plain"):
			plain = string(data)
		case strings.HasPrefix(mediaType, "text/html") && plain == "":
			html = string(data)
		}
	}

	if plain != "" {
		return plain, nil
	}
	return html, nil
}

func decodeHeader(s string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}
