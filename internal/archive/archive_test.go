package archive

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
)

// TestExportRoundTrip writes a small archive, then reads it back and
// asserts the manifest + per-item blobs survived intact. Locks in the
// on-disk format so future schema changes have to be deliberate.
func TestExportRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	fsDir := filepath.Join(tmp, "files")
	fs := filestore.New(fsDir)

	// Stage one file blob in the filestore so the file-typed item
	// has actual content to round-trip.
	hash, _, err := fs.Save(strings.NewReader("PDF DATA"))
	if err != nil {
		t.Fatalf("stage blob: %v", err)
	}

	// Stage a thumbnail.
	thumbsDir := filepath.Join(fsDir, "thumbnails")
	if err := writeFile(filepath.Join(thumbsDir, "01F.jpg"), []byte("THUMB")); err != nil {
		t.Fatalf("stage thumbnail: %v", err)
	}

	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	items := []model.Item{
		{
			ID: "01U", Type: model.TypeURL, Title: "YouTube",
			URL: "https://youtube.com/watch?v=abc",
			Notes: "video note", CreatedAt: now, UpdatedAt: now,
			Tags:        []model.Tag{{Name: "video"}, {Name: "watch"}},
			Collections: []model.Collection{{Name: "media"}},
		},
		{
			ID: "01S", Type: model.TypeSnippet, Title: "Snippet One",
			ExtractedText: "# Heading\n\nSome **markdown**.",
			CreatedAt:     now, UpdatedAt: now,
			Tags: []model.Tag{{Name: "notes"}},
		},
		{
			ID: "01F", Type: model.TypeFile, Title: "report.pdf",
			ContentHash: hash, MimeType: "application/pdf",
			SourcePath: "/private/tmp/report.pdf",
			FileSize:   int64(len("PDF DATA")),
			ThumbnailPath: "thumbnails/01F.jpg",
			CreatedAt:     now, UpdatedAt: now,
			Metadata: json.RawMessage("{}"),
		},
	}

	zipPath := filepath.Join(tmp, "out.zip")
	result, err := WriteZip(zipPath, ExportInput{
		Items:           items,
		Scope:           Scope{Type: "ids"},
		FileStore:       fs,
		ExporterVersion: "test",
	})
	if err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	if result.ItemCount != 3 {
		t.Errorf("ItemCount = %d, want 3", result.ItemCount)
	}
	// 3 blobs (url.txt, snippet.md, report.pdf) + 1 thumbnail.
	if result.BlobCount != 4 {
		t.Errorf("BlobCount = %d, want 4", result.BlobCount)
	}

	// Re-open and parse manifest.
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	manifest, err := ReadManifest(&zr.Reader)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Version != CurrentManifestVersion {
		t.Errorf("Version = %d, want %d", manifest.Version, CurrentManifestVersion)
	}
	if len(manifest.Items) != 3 {
		t.Fatalf("manifest items = %d, want 3", len(manifest.Items))
	}

	// URL item: url.txt content matches.
	urlEntry := findEntry(manifest.Items, "01U")
	if urlEntry == nil {
		t.Fatal("missing URL entry")
	}
	if got := readZipFile(t, &zr.Reader, urlEntry.BlobPath); got != "https://youtube.com/watch?v=abc\n" {
		t.Errorf("url.txt content = %q", got)
	}
	if got := urlEntry.Tags; len(got) != 2 || got[0] != "video" {
		t.Errorf("url tags = %v", got)
	}
	if got := urlEntry.Collections; len(got) != 1 || got[0] != "media" {
		t.Errorf("url collections = %v", got)
	}

	// Snippet item: extracted_text round-tripped, snippet.md present.
	snipEntry := findEntry(manifest.Items, "01S")
	if snipEntry == nil {
		t.Fatal("missing snippet entry")
	}
	if !strings.Contains(snipEntry.ExtractedText, "# Heading") {
		t.Errorf("extracted_text = %q", snipEntry.ExtractedText)
	}
	if !strings.HasSuffix(snipEntry.BlobPath, "snippet.md") {
		t.Errorf("snippet blob path = %q", snipEntry.BlobPath)
	}

	// File item: content_hash + filename preserved, blob bytes roundtrip.
	fileEntry := findEntry(manifest.Items, "01F")
	if fileEntry == nil {
		t.Fatal("missing file entry")
	}
	if got := readZipFile(t, &zr.Reader, fileEntry.BlobPath); got != "PDF DATA" {
		t.Errorf("file blob content = %q", got)
	}
	if !strings.HasSuffix(fileEntry.BlobPath, "report.pdf") {
		t.Errorf("file blob path = %q (expected report.pdf at end)", fileEntry.BlobPath)
	}
	if got := readZipFile(t, &zr.Reader, fileEntry.ThumbnailPath); got != "THUMB" {
		t.Errorf("thumbnail content = %q", got)
	}
}

// TestParseConflictPolicy guards the CLI surface: the only valid
// strings are the three documented values.
func TestParseConflictPolicy(t *testing.T) {
	for _, valid := range []string{"new-id", "skip", "replace"} {
		if _, err := ParseConflictPolicy(valid); err != nil {
			t.Errorf("ParseConflictPolicy(%q) errored: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "merge", "force", "rename"} {
		if _, err := ParseConflictPolicy(invalid); err == nil {
			t.Errorf("ParseConflictPolicy(%q) should have errored", invalid)
		}
	}
}

// TestRejectFutureManifestVersion guards forward compatibility:
// a manifest from a newer gostash should refuse to import rather
// than silently corrupting the local stash.
func TestRejectFutureManifestVersion(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "future.zip")
	fakeManifest := Manifest{
		Version: CurrentManifestVersion + 99,
		Items:   nil,
	}
	if err := writeZipWithManifest(zipPath, fakeManifest); err != nil {
		t.Fatalf("setup: %v", err)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if _, err := ReadManifest(&zr.Reader); err == nil {
		t.Error("ReadManifest should reject future-version manifests")
	}
}

// MARK: helpers

func findEntry(entries []Entry, id string) *Entry {
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i]
		}
	}
	return nil
}

func readZipFile(t *testing.T, zr *zip.Reader, path string) string {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == path {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			return string(data)
		}
	}
	t.Fatalf("zip entry %q not found", path)
	return ""
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeZipWithManifest(path string, m Manifest) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	w, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
