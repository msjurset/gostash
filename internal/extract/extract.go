package extract

import "io"

// Result holds the output of content extraction.
type Result struct {
	Text     string   // extracted plain text
	Title    string   // extracted title (if any)
	MimeType string   // detected MIME type
	Tags     []string // auto-suggested tags
}

// Extractor extracts searchable text from content.
type Extractor interface {
	Extract(r io.Reader, mimeType string) (*Result, error)
	Supports(mimeType string) bool
}

// registry holds all registered extractors in priority order.
var registry []Extractor

// Register adds an extractor to the registry.
func Register(e Extractor) {
	registry = append(registry, e)
}

// Run finds the first extractor that supports the given MIME type and runs it.
// Falls back to plain text if no specific extractor matches.
func Run(r io.Reader, mimeType string) (*Result, error) {
	for _, e := range registry {
		if e.Supports(mimeType) {
			return e.Extract(r, mimeType)
		}
	}
	// Fallback to text extractor
	return (&TextExtractor{}).Extract(r, mimeType)
}

func init() {
	Register(&PDFExtractor{})
	Register(&DocxExtractor{})
	Register(&ImageExtractor{})
	Register(&HTMLExtractor{})
	Register(&TextExtractor{})
}
