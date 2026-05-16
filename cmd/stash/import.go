package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/msjurset/gostash/internal/archive"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var importArchiveCmd = &cobra.Command{
	Use:   "archive <archive.zip>",
	Short: "Import items from a stash export archive",
	Long: `Read items out of a zip produced by 'stash export' and add them
to this stash. Tags and collections are merged by name; thumbnails
and blobs are copied into the local filestore.

Conflict policy when an imported item's ID already exists locally:

  --policy new-id   Generate a fresh ULID; both rows kept (default).
                    Round-trips into the same stash always succeed.
  --policy skip     Skip the imported row; existing row untouched.
  --policy replace  Delete the existing row + blob and import in
                    its place. Destructive — explicit opt-in only.

Filter flags trim what gets imported even if the archive contains more:

  --strip-tags          Drop all tag associations.
  --strip-collections   Drop all collection associations.
  --strip-archived      Skip rows that were archived in the source.`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importArchiveCmd.Flags().String("policy", "new-id", "Conflict policy: new-id, skip, replace")
	importArchiveCmd.Flags().Bool("strip-tags", false, "Don't import any tag associations")
	importArchiveCmd.Flags().Bool("strip-collections", false, "Don't import any collection associations")
	importArchiveCmd.Flags().Bool("strip-archived", false, "Skip items that were archived in the source")
	importCmd.AddCommand(importArchiveCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	policyStr, _ := cmd.Flags().GetString("policy")
	policy, err := archive.ParseConflictPolicy(policyStr)
	if err != nil {
		return err
	}
	stripTags, _ := cmd.Flags().GetBool("strip-tags")
	stripCols, _ := cmd.Flags().GetBool("strip-collections")
	stripArchived, _ := cmd.Flags().GetBool("strip-archived")

	zr, err := zip.OpenReader(args[0])
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer zr.Close()

	manifest, err := archive.ReadManifest(&zr.Reader)
	if err != nil {
		return err
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	summary := archive.ImportSummary{}

	for _, entry := range manifest.Items {
		if stripArchived && entry.Archived {
			summary.Skipped++
			continue
		}

		// Conflict resolution: figure out what id this row will use.
		finalID := entry.ID
		var existing *model.Item
		if entry.ID != "" {
			existing, _ = s.GetItem(ctx, entry.ID)
		}
		if existing != nil {
			switch policy {
			case archive.PolicySkip:
				summary.Skipped++
				continue
			case archive.PolicyReplace:
				if err := s.DeleteItem(ctx, existing.ID); err != nil {
					summary.Errors = append(summary.Errors,
						fmt.Sprintf("[%s] replace failed: %v", shortID(entry.ID), err))
					continue
				}
				if existing.ThumbnailPath != "" {
					_ = fs.RemoveRelative(existing.ThumbnailPath)
				}
				// Refcount-guard the blob delete — if another item
				// (perhaps imported earlier in the same archive)
				// shares this content_hash, leave the bytes in place.
				if existing.ContentHash != "" {
					if refs, err := s.CountItemsByContentHash(ctx, existing.ContentHash); err == nil && refs == 0 {
						_ = fs.Delete(existing.ContentHash)
					}
				}
				summary.Replaced++
			case archive.PolicyNewID:
				finalID = newULID()
				summary.Reassigned++
			}
		}

		item, err := buildItemFromEntry(&zr.Reader, entry, finalID, fs, stripTags, stripCols)
		if err != nil {
			summary.Errors = append(summary.Errors,
				fmt.Sprintf("[%s] %v", shortID(entry.ID), err))
			continue
		}

		if err := s.CreateItem(ctx, item); err != nil {
			summary.Errors = append(summary.Errors,
				fmt.Sprintf("[%s] create: %v", shortID(entry.ID), err))
			continue
		}
		// Tags/collections: AddTag / AddToCollection are idempotent
		// against missing rows so we can call them after CreateItem.
		if !stripTags {
			for _, tag := range entry.Tags {
				if err := s.AddTag(ctx, finalID, tag); err != nil {
					summary.Errors = append(summary.Errors,
						fmt.Sprintf("[%s] add tag %s: %v", shortID(finalID), tag, err))
				}
			}
		}
		if !stripCols {
			for _, col := range entry.Collections {
				if err := s.AddToCollection(ctx, finalID, col); err != nil {
					summary.Errors = append(summary.Errors,
						fmt.Sprintf("[%s] add collection %s: %v", shortID(finalID), col, err))
				}
			}
		}
		summary.Imported++
	}

	if flagJSON {
		printJSON(summary)
		return nil
	}

	fmt.Printf("Imported %d items", summary.Imported)
	if summary.Reassigned > 0 {
		fmt.Printf(" (%d reassigned new IDs)", summary.Reassigned)
	}
	if summary.Replaced > 0 {
		fmt.Printf(", replaced %d", summary.Replaced)
	}
	if summary.Skipped > 0 {
		fmt.Printf(", skipped %d", summary.Skipped)
	}
	fmt.Println()
	if len(summary.Errors) > 0 {
		fmt.Printf("\n%d errors:\n", len(summary.Errors))
		for _, e := range summary.Errors {
			fmt.Println("  " + e)
		}
		return fmt.Errorf("import completed with %d errors", len(summary.Errors))
	}
	return nil
}

// buildItemFromEntry reconstructs a model.Item from a manifest entry,
// re-saving any blob into the local filestore (which assigns a fresh
// content_hash) and copying the thumbnail in. Tags / collections are
// not added here — the caller does that after CreateItem succeeds.
func buildItemFromEntry(
	r *zip.Reader,
	entry archive.Entry,
	finalID string,
	fs *filestore.FileStore,
	_ bool, _ bool,
) (*model.Item, error) {
	// We want a fresh CreatedAt only when we reassigned the id;
	// otherwise honor the source manifest so timeline ordering is
	// preserved on round-trip imports.
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := entry.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	item := &model.Item{
		ID:            finalID,
		Type:          model.ItemType(entry.Type),
		Title:         entry.Title,
		URL:           entry.URL,
		Notes:         entry.Notes,
		ExtractedText: entry.ExtractedText,
		MimeType:      entry.MimeType,
		FileSize:      entry.FileSize,
		SourcePath:    entry.SourcePath,
		Metadata:      json.RawMessage("{}"),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		Archived:      entry.Archived,
	}

	// Blob: file/image/email content. Save to local filestore to
	// recompute content_hash (the source hash is meaningless if
	// we changed any byte in transit; recomputing also dedupes
	// against existing local content).
	if entry.BlobPath != "" {
		if file := archive.FileByPath(r, entry.BlobPath); file != nil {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open blob: %w", err)
			}
			switch item.Type {
			case model.TypeFile, model.TypeImage, model.TypeEmail:
				hash, size, err := fs.Save(rc)
				rc.Close()
				if err != nil {
					return nil, fmt.Errorf("save blob: %w", err)
				}
				item.ContentHash = hash
				if item.FileSize == 0 {
					item.FileSize = size
				}
			default:
				// URL items have a `url.txt`; snippet items have
				// `snippet.md`. Both are already mirrored in
				// `entry.URL` / `entry.ExtractedText`, so we don't
				// need to read them from the zip.
				rc.Close()
			}
		}
	}

	// Thumbnail: copy into <baseDir>/thumbnails/<finalID>.<ext>.
	if entry.ThumbnailPath != "" {
		if file := archive.FileByPath(r, entry.ThumbnailPath); file != nil {
			ext := filepath.Ext(entry.ThumbnailPath)
			if ext == "" {
				ext = ".jpg"
			}
			thumbsDir := filepath.Join(fs.BaseDir(), "thumbnails")
			if err := os.MkdirAll(thumbsDir, 0o755); err == nil {
				rel := filepath.Join("thumbnails", finalID+ext)
				dest := filepath.Join(fs.BaseDir(), rel)
				out, err := os.Create(dest)
				if err == nil {
					rc, err := file.Open()
					if err == nil {
						_, _ = io.Copy(out, rc)
						rc.Close()
					}
					out.Close()
					item.ThumbnailPath = rel
				}
			}
		}
	}

	return item, nil
}

func newULID() string {
	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	return ulid.MustNew(ulid.Timestamp(now), entropy).String()
}

// Compile-time assertion the store interface we use is the right one.
var _ store.Store = (*nilStore)(nil)

type nilStore struct{ store.Store }
