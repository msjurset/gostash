package extract

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ImageExtractor performs OCR on images using tesseract.
type ImageExtractor struct{}

func (e *ImageExtractor) Supports(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

func (e *ImageExtractor) Extract(r io.Reader, mimeType string) (*Result, error) {
	if !tesseractAvailable() {
		return &Result{
			MimeType: mimeType,
			Tags:     []string{"image"},
		}, nil
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	text, err := runTesseract(data)
	if err != nil {
		// OCR failure is non-fatal — return empty text
		return &Result{
			MimeType: mimeType,
			Tags:     []string{"image"},
		}, nil
	}

	return &Result{
		Text:     strings.TrimSpace(text),
		MimeType: mimeType,
		Tags:     []string{"image"},
	}, nil
}

func tesseractAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}

// runTesseract pipes image data to tesseract via stdin and captures stdout.
func runTesseract(data []byte) (string, error) {
	cmd := exec.Command("tesseract", "stdin", "stdout", "--psm", "3")
	cmd.Stdin = bytes.NewReader(data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %s", stderr.String())
	}

	return stdout.String(), nil
}
