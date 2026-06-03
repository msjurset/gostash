package extract

import (
	"io"
	"strings"

	"github.com/msjurset/gostash/internal/identify"
)

// VideoExtractor handles video files.
// Today it just identifies them and adds a "video" tag;
// full multimodal transcription is handled in the background
// by the identify.Worker.
type VideoExtractor struct{}

func (e *VideoExtractor) Supports(mimeType string) bool {
	return strings.HasPrefix(mimeType, "video/")
}

func (e *VideoExtractor) Extract(r io.Reader, mimeType string, opts Options) (*Result, error) {
	tags := []string{"video"}
	if opts.TranscribeVideo {
		tags = append(tags, identify.Tag)
	}
	return &Result{
		MimeType: mimeType,
		Tags:     tags,
	}, nil
}
