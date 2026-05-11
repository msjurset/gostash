// Package archive defines the on-disk format gostash uses to export
// and import items as zip archives. The format is a single
// `manifest.json` at the archive root plus per-item subdirectories
// holding any blobs (file/image content, snippet markdown, URL
// text, thumbnails). Manifests are versioned so future format
// changes can stay backward-compatible.
//
// Layout:
//
//	stash-export-YYYY-MM-DD.zip
//	├── manifest.json
//	├── 01ABC…/
//	│   ├── content.pdf       # files/images: original blob
//	│   ├── snippet.md        # snippets: extracted_text as md
//	│   ├── url.txt           # link items: just the URL
//	│   └── thumbnail.jpg     # if present
//	└── 01DEF…/
//	    └── …
package archive

import "time"

// CurrentManifestVersion is the on-disk schema version. Bump when
// making backward-incompatible changes; importers must accept any
// version they understand and reject newer ones with a clear error.
const CurrentManifestVersion = 1

// Manifest is the top-level structure serialized to manifest.json.
type Manifest struct {
	Version         int       `json:"version"`
	ExportedAt      time.Time `json:"exported_at"`
	Exporter        string    `json:"exporter"`         // "gostash"
	ExporterVersion string    `json:"exporter_version"` // CLI version string
	Scope           Scope     `json:"scope"`
	Items           []Entry   `json:"items"`
}

// Scope captures what subset of the stash this archive contains —
// useful for the importer's UI ("import 17 items from tag #video")
// and for distinguishing "I exported a single item" from "I exported
// the whole archive."
type Scope struct {
	Type  string `json:"type"`            // "ids" | "tag" | "collection" | "all"
	Value string `json:"value,omitempty"` // tag name or collection name; empty for ids/all
}

// Entry is one item record inside the manifest. Fields that don't
// apply to the item's type are omitted via omitempty so the manifest
// stays compact.
type Entry struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"` // link | snippet | file | image | email
	Title         string    `json:"title"`
	URL           string    `json:"url,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	ExtractedText string    `json:"extracted_text,omitempty"`
	MimeType      string    `json:"mime_type,omitempty"`
	FileSize      int64     `json:"file_size,omitempty"`
	ContentHash   string    `json:"content_hash,omitempty"`
	SourcePath    string    `json:"source_path,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Collections   []string  `json:"collections,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Archived      bool      `json:"archived,omitempty"`

	// BlobPath is the path inside the zip pointing at the item's
	// content blob, when one exists. Snippets and URLs always have
	// `snippet.md` / `url.txt`; files and images carry their
	// original (sanitized) filename; emails carry `message.eml`.
	BlobPath string `json:"blob_path,omitempty"`

	// ThumbnailPath is the path inside the zip for the item's
	// canonical thumbnail (if any was set on the source item).
	ThumbnailPath string `json:"thumbnail_path,omitempty"`
}
