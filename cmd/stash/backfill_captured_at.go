package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var backfillCapturedAtCmd = &cobra.Command{
	Use:   "backfill-captured-at [id]",
	Short: "Fill items.captured_at from EXIF / email headers / filesystem time",
	Long: `Walk items, derive the best capture timestamp for each, and write
it to items.captured_at. Idempotent — items that already have a
captured_at set are skipped unless --force is passed.

Resolution order per type:
  image    EXIF DateTimeOriginal → file mtime → leave NULL
  file     file mtime → leave NULL
  email    most recent Date / Received header → file mtime → leave NULL
  snippet  the row's own created_at (snippets have no separate
           capture moment; this just normalizes the column)
  url      always leave NULL — URL items have no reliable signal

Examples:
  stash backfill-captured-at 01KR9HKVDG    # one item
  stash backfill-captured-at --all          # every item missing captured_at
  stash backfill-captured-at --all --dry-run
  stash backfill-captured-at --all --force  # rewrite even when populated`,
	RunE: runBackfillCapturedAt,
}

func init() {
	backfillCapturedAtCmd.Flags().Bool("all", false, "Process every item without captured_at")
	backfillCapturedAtCmd.Flags().Bool("dry-run", false, "Report what would change without writing")
	backfillCapturedAtCmd.Flags().Bool("force", false, "Rewrite captured_at even when already populated")
	rootCmd.AddCommand(backfillCapturedAtCmd)
}

func runBackfillCapturedAt(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	if all && len(args) > 0 {
		return fmt.Errorf("specify either <id> or --all, not both")
	}
	if !all && len(args) == 0 {
		return fmt.Errorf("specify an item id or pass --all")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	targets, err := capturedAtTargets(ctx, s, args, all, force)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if flagJSON {
			printJSON(map[string]any{"updated": 0, "skipped": 0, "errors": 0})
		} else {
			fmt.Println("Nothing to backfill.")
		}
		return nil
	}

	if dryRun {
		if flagJSON {
			out := make([]map[string]string, 0, len(targets))
			for _, t := range targets {
				out = append(out, map[string]string{
					"id":    t.ID,
					"type":  string(t.Type),
					"title": t.Title,
				})
			}
			printJSON(map[string]any{"would_process": out})
		} else {
			fmt.Printf("Would process %d item(s) for captured_at.\n", len(targets))
			for _, t := range targets {
				fmt.Printf("  [%s] %s — %s\n", shortID(t.ID), t.Type.Display(), t.Title)
			}
		}
		return nil
	}

	updated := 0
	skipped := 0
	var errs []string
	for _, item := range targets {
		captured, source, err := deriveCapturedAt(ctx, s, fs, item)
		if err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", shortID(item.ID), err))
			if !flagJSON {
				fmt.Printf("  ✗ [%s] %s — %v\n", shortID(item.ID), item.Title, err)
			}
			continue
		}
		if captured == nil {
			skipped++
			if !flagJSON {
				fmt.Printf("  - [%s] %s — no capture signal\n", shortID(item.ID), item.Title)
			}
			continue
		}
		item.CapturedAt = captured
		if err := s.UpdateItem(ctx, &item); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] update: %v", shortID(item.ID), err))
			continue
		}
		updated++
		if !flagJSON {
			fmt.Printf("  ✓ [%s] %s → %s (%s)\n",
				shortID(item.ID), item.Title,
				captured.Format("2006-01-02 15:04"),
				source)
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"updated": updated,
			"skipped": skipped,
			"errors":  len(errs),
		})
	} else {
		fmt.Printf("\nUpdated %d, skipped %d, %d error(s).\n", updated, skipped, len(errs))
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s)", len(errs))
	}
	return nil
}

// capturedAtTargets gathers the items the backfill should look at —
// a single id or every eligible item. URL items are always excluded
// because there's no reliable capture signal for them (the URL
// itself is the "content" and was created on someone else's
// timeline).
func capturedAtTargets(ctx context.Context, s store.Store, args []string, all, force bool) ([]model.Item, error) {
	if !all {
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return nil, err
		}
		if item.Type == model.TypeURL {
			return nil, fmt.Errorf("url items have no capture signal — captured_at is intentionally left NULL")
		}
		if item.CapturedAt != nil && !force {
			return nil, fmt.Errorf("item already has captured_at=%s; pass --force to overwrite",
				item.CapturedAt.Format("2006-01-02 15:04"))
		}
		return []model.Item{*item}, nil
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 0, IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var targets []model.Item
	for _, item := range items {
		if item.Type == model.TypeURL {
			continue
		}
		if item.CapturedAt != nil && !force {
			continue
		}
		targets = append(targets, item)
	}
	return targets, nil
}

// deriveCapturedAt computes the best capture timestamp for an item,
// returning (nil, "", nil) when no signal is available — the caller
// reports that as "skipped" rather than an error. The string return
// names the source ("exif", "fs", "email-headers", "created_at") so
// the dry-run / verbose output can explain where the value came
// from.
func deriveCapturedAt(
	ctx context.Context,
	s store.Store,
	fs *filestore.FileStore,
	item model.Item,
) (*time.Time, string, error) {
	switch item.Type {
	case model.TypeSnippet:
		// Snippets have no separate capture moment — the row's own
		// CreatedAt IS the capture. Just normalize the column.
		t := item.CreatedAt
		return &t, "created_at", nil

	case model.TypeImage:
		// EXIF DateTimeOriginal walking primary + attached blobs
		// (same pattern as backfill-locations). Fall back to file
		// mtime when EXIF doesn't yield a parseable time.
		if t := exifCaptureFromHashes(fs, allHashes(ctx, s, item)); t != nil {
			return t, "exif", nil
		}
		if t := blobMTime(fs, item.ContentHash); t != nil {
			return t, "fs", nil
		}
		return nil, "", nil

	case model.TypeFile:
		// Files have no internal-EXIF analogue — use the blob's
		// mtime, which is the closest "when was this last
		// meaningfully changed" signal available.
		if t := blobMTime(fs, item.ContentHash); t != nil {
			return t, "fs", nil
		}
		return nil, "", nil

	case model.TypeEmail:
		// Re-run the email extractor over the stored blob so it
		// can parse Date / Received headers exactly as the live
		// ingest path does. Cheap — emails are small.
		if t := emailCaptureFromBlob(fs, item.ContentHash); t != nil {
			return t, "email-headers", nil
		}
		if t := blobMTime(fs, item.ContentHash); t != nil {
			return t, "fs", nil
		}
		return nil, "", nil
	}
	return nil, "", nil
}

// allHashes returns every content hash associated with an item —
// the primary plus any attached files. Tries the in-memory Files
// slice first (populated by GetItem), falls back to a fresh
// ListItemFiles when the caller passed a list-derived Item.
func allHashes(ctx context.Context, s store.Store, item model.Item) []string {
	out := []string{item.ContentHash}
	if len(item.Files) > 0 {
		for _, f := range item.Files {
			if f.ContentHash != "" {
				out = append(out, f.ContentHash)
			}
		}
		return out
	}
	if extra, err := s.ListItemFiles(ctx, item.ID); err == nil {
		for _, f := range extra {
			if f.ContentHash != "" {
				out = append(out, f.ContentHash)
			}
		}
	}
	return out
}

func exifCaptureFromHashes(fs *filestore.FileStore, hashes []string) *time.Time {
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		f, err := fs.Open(hash)
		if err != nil {
			continue
		}
		t, err := exif.ExtractCaptureTime(f)
		f.Close()
		if err == nil && !t.IsZero() {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

func blobMTime(fs *filestore.FileStore, hash string) *time.Time {
	if hash == "" {
		return nil
	}
	path := fs.Path(hash)
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	t := info.ModTime().UTC()
	return &t
}

func emailCaptureFromBlob(fs *filestore.FileStore, hash string) *time.Time {
	if hash == "" {
		return nil
	}
	f, err := fs.Open(hash)
	if err != nil {
		return nil
	}
	defer f.Close()
	ex := &extract.EmailExtractor{}
	res, err := ex.Extract(f, extract.MIMEEmail)
	if err != nil || res == nil || res.CapturedAt == nil {
		return nil
	}
	t := res.CapturedAt.UTC()
	return &t
}
