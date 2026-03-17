package extract

import (
	"net/http"
	"path/filepath"
	"strings"
)

// MIME type constants
const (
	MIMETextPlain = "text/plain"
	MIMEHTML      = "text/html"
	MIMEPDF       = "application/pdf"
	MIMEPNG       = "image/png"
	MIMEJPEG      = "image/jpeg"
	MIMEGIF       = "image/gif"
	MIMEWebP      = "image/webp"
)

// DetectMIME determines the MIME type from file content and name.
func DetectMIME(data []byte, filename string) string {
	// Try content-based detection first
	if len(data) > 0 {
		ct := http.DetectContentType(data)
		if ct != "application/octet-stream" {
			return ct
		}
	}
	// Fall back to extension
	return mimeFromExt(filepath.Ext(filename))
}

// SuggestTags returns auto-tag suggestions based on MIME type.
func SuggestTags(mimeType string) []string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return []string{"image"}
	case strings.Contains(mimeType, "pdf"):
		return []string{"pdf", "document"}
	case strings.Contains(mimeType, "wordprocessingml") || strings.Contains(mimeType, "msword"):
		return []string{"docx", "document"}
	case strings.Contains(mimeType, "html"):
		return []string{"web"}
	default:
		return nil
	}
}

func mimeFromExt(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".html", ".htm":
		return MIMEHTML
	case ".txt", ".md", ".rst":
		return MIMETextPlain
	case ".pdf":
		return MIMEPDF
	case ".png":
		return MIMEPNG
	case ".jpg", ".jpeg":
		return MIMEJPEG
	case ".gif":
		return MIMEGIF
	case ".webp":
		return MIMEWebP
	case ".docx":
		return MIMEDocx
	default:
		return "application/octet-stream"
	}
}
