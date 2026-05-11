package archive

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ConflictPolicy decides what to do when an imported item's ID
// already exists in the destination stash.
//
//   - PolicyNewID  — generate a fresh ULID for the imported item;
//     keep all other metadata. Default — round-trips into the same
//     stash always succeed.
//   - PolicySkip   — skip the imported item; leave the existing one
//     untouched. The summary reports skipped count.
//   - PolicyReplace— delete the existing item (and its blob /
//     thumbnail) and import in its place. Destructive; explicit
//     opt-in only.
type ConflictPolicy string

const (
	PolicyNewID   ConflictPolicy = "new-id"
	PolicySkip    ConflictPolicy = "skip"
	PolicyReplace ConflictPolicy = "replace"
)

// ParseConflictPolicy validates a CLI/UI string and returns the
// canonical ConflictPolicy or an error listing the valid options.
func ParseConflictPolicy(s string) (ConflictPolicy, error) {
	switch ConflictPolicy(s) {
	case PolicyNewID, PolicySkip, PolicyReplace:
		return ConflictPolicy(s), nil
	}
	return "", fmt.Errorf("invalid policy %q (want new-id, skip, or replace)", s)
}

// ReadManifest opens a zip archive, reads its `manifest.json`, and
// returns the parsed manifest. Caller is responsible for closing
// `*zip.ReadCloser` if they intend to read blob entries afterward
// (typical pattern: get the reader once via `OpenArchive`, call
// `ReadManifest` on it, then iterate through entries).
func ReadManifest(r *zip.Reader) (Manifest, error) {
	for _, f := range r.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Manifest{}, fmt.Errorf("open manifest: %w", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return Manifest{}, fmt.Errorf("read manifest: %w", err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return Manifest{}, fmt.Errorf("parse manifest: %w", err)
		}
		if m.Version > CurrentManifestVersion {
			return Manifest{}, fmt.Errorf(
				"manifest version %d exceeds supported version %d — upgrade gostash",
				m.Version, CurrentManifestVersion,
			)
		}
		return m, nil
	}
	return Manifest{}, errors.New("manifest.json not found in archive")
}

// FileByPath finds a zip entry by its path and returns an opener.
// Returns (nil, nil) when the path isn't in the archive — used for
// "BlobPath set in manifest but file missing" tolerance during
// import.
func FileByPath(r *zip.Reader, path string) *zip.File {
	if path == "" {
		return nil
	}
	for _, f := range r.File {
		if f.Name == path {
			return f
		}
	}
	return nil
}

// ImportSummary describes what an import call did. Returned to both
// the CLI and Mac sides for status display.
type ImportSummary struct {
	Imported   int      `json:"imported"`
	Skipped    int      `json:"skipped"`
	Replaced   int      `json:"replaced"`
	Reassigned int      `json:"reassigned"` // count of items given fresh IDs under PolicyNewID
	Errors     []string `json:"errors,omitempty"`
}
