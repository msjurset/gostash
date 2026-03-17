package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

const (
	MIMEDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// DocxExtractor extracts text from DOCX files.
type DocxExtractor struct{}

func (e *DocxExtractor) Supports(mimeType string) bool {
	return strings.Contains(mimeType, "wordprocessingml") ||
		strings.Contains(mimeType, "docx") ||
		strings.Contains(mimeType, "msword")
}

func (e *DocxExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read docx: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open docx zip: %w", err)
	}

	text, err := extractDocxText(zr)
	if err != nil {
		return nil, fmt.Errorf("extract docx text: %w", err)
	}

	return &Result{
		Text:     text,
		MimeType: MIMEDocx,
		Tags:     []string{"docx", "document"},
	}, nil
}

// extractDocxText reads word/document.xml from the zip and pulls text
// from all <w:t> elements.
func extractDocxText(zr *zip.Reader) (string, error) {
	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("word/document.xml not found in archive")
	}

	rc, err := docFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var text strings.Builder
	decoder := xml.NewDecoder(rc)
	var inParagraph bool

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if text.Len() > 0 && inParagraph {
					text.WriteString("\n")
				}
				inParagraph = true
			}
		case xml.CharData:
			text.Write(t)
		}
	}

	return strings.TrimSpace(text.String()), nil
}
