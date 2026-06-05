package extract

import (
	"fmt"
	"io"
	"time"
)

// Result holds the output of content extraction.
type Result struct {
	Text     string   // extracted plain text
	Title    string   // extracted title (if any)
	MimeType string   // detected MIME type
	Tags     []string // auto-suggested tags
	// CapturedAt is the best content-creation timestamp the
	// extractor could derive. Today only the email extractor sets
	// it (from the Date / Received headers); other extractors
	// leave it nil and let the caller fall back to filesystem
	// time or EXIF.
	CapturedAt *time.Time
}

// Options control how extraction is performed.
type Options struct {
	// TranscribeVideo, when true, tells the video extractor to tag
	// the item for AI transcription. Default false (videos just
	// get tagged "video" but stay idle to avoid accidental cost).
	TranscribeVideo bool
}

// Extractor extracts searchable text from content.
type Extractor interface {
	Extract(r io.Reader, mimeType string, opts Options) (*Result, error)
	Supports(mimeType string) bool
}

// registry holds all registered extractors in priority order.
var registry []Extractor

// Register adds an extractor to the registry.
func Register(e Extractor) {
	registry = append(registry, e)
}

// Run finds the first extractor that supports the given MIME type and runs it.
func Run(r io.Reader, mimeType string, opts Options) (*Result, error) {
	for _, e := range registry {
		if e.Supports(mimeType) {
			return e.Extract(r, mimeType, opts)
		}
	}
	
	// Only fall back to text extractor if it's actually text OR generic binary.
	// We don't want to try and "extract text" from video/mp4 etc.
	text := &TextExtractor{}
	if text.Supports(mimeType) || mimeType == "application/octet-stream" {
		return text.Extract(r, mimeType, opts)
	}

	return nil, fmt.Errorf("no extractor for mime type: %s", mimeType)
}

func init() {
	Register(&EmailExtractor{})
	Register(&PDFExtractor{})
	Register(&DocxExtractor{})
	Register(&ImageExtractor{})
	Register(&VideoExtractor{})
	Register(&HTMLExtractor{})
	Register(&TextExtractor{})
}
