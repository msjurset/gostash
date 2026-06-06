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
	var ct string
	if len(data) > 0 {
		ct = http.DetectContentType(data)
	}

	// If extension maps to a more specific type than content detection
	// produced, prefer the extension. This handles formats like .eml
	// whose content looks like text/plain to http.DetectContentType.
	ext := filepath.Ext(filename)
	if extMIME := mimeFromExt(ext); extMIME != "application/octet-stream" {
		if ct == "" || ct == "application/octet-stream" || strings.HasPrefix(ct, "text/plain") {
			return extMIME
		}
	}

	if ct != "" {
		return ct
	}
	return "application/octet-stream"
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
	case mimeType == MIMEEmail:
		return []string{"email"}
	case strings.Contains(mimeType, "gzip") || strings.Contains(mimeType, "tar") || strings.Contains(mimeType, "zip"):
		return []string{"archive"}
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
	case ".eml":
		return MIMEEmail
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	case ".rar":
		return "application/x-rar-compressed"
	case ".7z":
		return "application/x-7z-compressed"
	default:
		return "application/octet-stream"
	}
}
