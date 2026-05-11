package archive

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
)

// ExportInput is the bundle of items + filestore + scope needed to
// produce one archive. Caller is responsible for resolving the item
// list (by ids / tag / collection); we just write what's handed in.
type ExportInput struct {
	Items           []model.Item
	Scope           Scope
	FileStore       *filestore.FileStore
	ExporterVersion string // populated from the CLI's `version` global
}

// ExportResult summarizes what got written to disk so callers
// (CLI, Mac app) can show a sensible confirmation message.
type ExportResult struct {
	Path       string `json:"path"`
	ItemCount  int    `json:"item_count"`
	BlobCount  int    `json:"blob_count"`
	TotalBytes int64  `json:"total_bytes"`
}

// WriteZip serializes `input` to a zip archive at `outPath`. Existing
// file at `outPath` is overwritten. Item subdirectories are named by
// the item's full ID for stability across exports.
func WriteZip(outPath string, input ExportInput) (ExportResult, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return ExportResult{}, fmt.Errorf("mkdir output: %w", err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("create zip: %w", err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	manifest := Manifest{
		Version:         CurrentManifestVersion,
		ExportedAt:      time.Now().UTC(),
		Exporter:        "gostash",
		ExporterVersion: input.ExporterVersion,
		Scope:           input.Scope,
		Items:           make([]Entry, 0, len(input.Items)),
	}

	var totalBytes int64
	blobCount := 0

	for _, item := range input.Items {
		entry := entryFromItem(item)
		dir := item.ID

		// Per-type blob output.
		switch item.Type {
		case model.TypeURL:
			if item.URL != "" {
				rel := filepath.Join(dir, "url.txt")
				if n, err := writeFileToZip(zw, rel, strings.NewReader(item.URL+"\n")); err != nil {
					return ExportResult{}, err
				} else {
					totalBytes += n
					blobCount++
					entry.BlobPath = rel
				}
			}
		case model.TypeSnippet:
			if item.ExtractedText != "" {
				rel := filepath.Join(dir, "snippet.md")
				if n, err := writeFileToZip(zw, rel, strings.NewReader(item.ExtractedText)); err != nil {
					return ExportResult{}, err
				} else {
					totalBytes += n
					blobCount++
					entry.BlobPath = rel
				}
			}
		case model.TypeFile, model.TypeImage, model.TypeEmail:
			if item.ContentHash != "" {
				name := blobFilename(item)
				rel := filepath.Join(dir, name)
				rc, err := input.FileStore.Open(item.ContentHash)
				if err != nil {
					// Don't abort the whole export over one missing
					// blob — record the entry without BlobPath so
					// the importer at least gets the metadata.
					rc = nil
				}
				if rc != nil {
					if n, err := writeReaderToZip(zw, rel, rc); err != nil {
						rc.Close()
						return ExportResult{}, err
					} else {
						totalBytes += n
						blobCount++
						entry.BlobPath = rel
					}
					rc.Close()
				}
			}
		}

		// Thumbnail (any item type can have one).
		if item.ThumbnailPath != "" {
			thumbAbs := input.FileStore.ResolveRelative(item.ThumbnailPath)
			f, err := os.Open(thumbAbs)
			if err == nil {
				name := "thumbnail" + filepath.Ext(item.ThumbnailPath)
				rel := filepath.Join(dir, name)
				if n, err := writeReaderToZip(zw, rel, f); err == nil {
					totalBytes += n
					blobCount++
					entry.ThumbnailPath = rel
				}
				f.Close()
			}
		}

		manifest.Items = append(manifest.Items, entry)
	}

	// Manifest goes last so the writer has all per-item BlobPath /
	// ThumbnailPath populated.
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ExportResult{}, fmt.Errorf("marshal manifest: %w", err)
	}
	if _, err := writeBytesToZip(zw, "manifest.json", data); err != nil {
		return ExportResult{}, err
	}

	if err := zw.Close(); err != nil {
		return ExportResult{}, fmt.Errorf("finalize zip: %w", err)
	}

	return ExportResult{
		Path:       outPath,
		ItemCount:  len(manifest.Items),
		BlobCount:  blobCount,
		TotalBytes: totalBytes,
	}, nil
}

// entryFromItem converts a stored model into the manifest entry shape.
// Tag/collection associations flatten to name-only since IDs are
// per-stash and meaningless across machines.
func entryFromItem(item model.Item) Entry {
	tags := make([]string, 0, len(item.Tags))
	for _, t := range item.Tags {
		tags = append(tags, t.Name)
	}
	cols := make([]string, 0, len(item.Collections))
	for _, c := range item.Collections {
		cols = append(cols, c.Name)
	}
	return Entry{
		ID:            item.ID,
		Type:          string(item.Type),
		Title:         item.Title,
		URL:           item.URL,
		Notes:         item.Notes,
		ExtractedText: item.ExtractedText,
		MimeType:      item.MimeType,
		FileSize:      item.FileSize,
		ContentHash:   item.ContentHash,
		SourcePath:    item.SourcePath,
		Tags:          tags,
		Collections:   cols,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
		Archived:      item.Archived,
	}
}

// blobFilename picks a sensible filename for a file/image/email blob
// inside the archive. Order of preference:
//  1. Basename of `source_path` (sanitized) — preserves the user's
//     original filename across the export.
//  2. Basename of title with the right extension appended.
//  3. Generic name + extension derived from MIME type.
func blobFilename(item model.Item) string {
	if item.SourcePath != "" {
		base := filepath.Base(item.SourcePath)
		if base != "" && base != "." && base != "/" {
			return sanitizeFilename(base)
		}
	}
	if item.Title != "" {
		ext := filepath.Ext(item.Title)
		if ext == "" {
			ext = extFromMime(item.MimeType)
		}
		clean := sanitizeFilename(item.Title)
		if filepath.Ext(clean) == "" {
			clean += ext
		}
		if clean != "" && clean != ext {
			return clean
		}
	}
	switch item.Type {
	case model.TypeEmail:
		return "message.eml"
	case model.TypeImage:
		return "image" + extFromMime(item.MimeType)
	default:
		return "content" + extFromMime(item.MimeType)
	}
}

// sanitizeFilename strips path separators, NUL, and control chars
// so the resulting name is safe to use as a zip entry / on-disk
// filename across platforms. Caps at 200 chars.
func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r < 32 {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > 200 {
		cleaned = cleaned[:200]
	}
	if cleaned == "" {
		return "blob"
	}
	return cleaned
}

func extFromMime(mime string) string {
	if mime == "" {
		return ""
	}
	mime = strings.ToLower(strings.SplitN(mime, ";", 2)[0])
	switch mime {
	case "application/pdf":
		return ".pdf"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "text/html":
		return ".html"
	case "message/rfc822":
		return ".eml"
	}
	if slash := strings.Index(mime, "/"); slash > 0 {
		return "." + mime[slash+1:]
	}
	return ""
}

func writeFileToZip(zw *zip.Writer, name string, r io.Reader) (int64, error) {
	w, err := zw.Create(name)
	if err != nil {
		return 0, fmt.Errorf("zip create %s: %w", name, err)
	}
	return io.Copy(w, r)
}

func writeReaderToZip(zw *zip.Writer, name string, r io.Reader) (int64, error) {
	return writeFileToZip(zw, name, r)
}

func writeBytesToZip(zw *zip.Writer, name string, data []byte) (int64, error) {
	w, err := zw.Create(name)
	if err != nil {
		return 0, fmt.Errorf("zip create %s: %w", name, err)
	}
	n, err := w.Write(data)
	return int64(n), err
}
