package extract

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFExtractor extracts text from PDF files.
type PDFExtractor struct{}

func (e *PDFExtractor) Supports(mimeType string) bool {
	return strings.Contains(mimeType, "pdf")
}

func (e *PDFExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	// pdf library needs ReadSeeker, so buffer into memory
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse pdf: %w", err)
	}

	var text strings.Builder
	numPages := reader.NumPage()
	for i := 1; i <= numPages; i++ {
		p := reader.Page(i)
		if p.V.IsNull() {
			continue
		}
		content, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		text.WriteString(content)
		if i < numPages {
			text.WriteString("\n")
		}
	}

	return &Result{
		Text:     text.String(),
		MimeType: MIMEPDF,
		Tags:     []string{"pdf", "document"},
	}, nil
}
