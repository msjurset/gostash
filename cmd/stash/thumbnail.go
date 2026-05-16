package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/thumbsync"
	"github.com/spf13/cobra"
)

var thumbnailCmd = &cobra.Command{
	Use:   "thumbnail",
	Short: "Manage per-item thumbnail images",
	Long: `Set, clear, and inspect the per-item thumbnail used in the
detail view, list rows, and (eventually) grid view. The CLI does not
post-process images — callers are expected to hand in a properly sized
artifact (the macOS app runs the saliency-crop / sRGB / JPEG pipeline
before invoking the CLI).`,
}

var thumbnailSetCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Set an item's thumbnail from a local file or remote URL",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailSet,
}

var thumbnailClearCmd = &cobra.Command{
	Use:   "clear <id>",
	Short: "Remove an item's thumbnail",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailClear,
}

var thumbnailPathCmd = &cobra.Command{
	Use:   "path <id>",
	Short: "Print the absolute path to an item's thumbnail (empty if unset)",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailPath,
}

var thumbnailBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Import thumbnails for every URL item missing one",
	Long: `Scan all URL-type items whose thumbnail_path is empty and run the
same fetch + extract pipeline as 'stash thumbnail import' against each
one. Use after capturing URLs from the browser extension or the HTTP
serve /capture endpoint, neither of which currently extracts thumbnails
at capture time.

Failures (404s, paywalls, no candidate images) don't abort the run —
they're counted and reported at the end. Safe to re-run; items that
got a thumbnail in a prior run are skipped.`,
	RunE: runThumbnailBackfill,
}

var thumbnailImportCmd = &cobra.Command{
	Use:   "import <id>",
	Short: "Fetch a URL and import its best thumbnail candidate",
	Long: `Fetch a URL and import its best thumbnail. The source URL defaults
to the item's own URL but --from overrides — useful for harvesting an
image from a different page or a direct image URL when the stashed
link doesn't have a great hero image.

Response branching:
  image/*   → use the response body directly
  text/html → parse og:image, twitter:image, schema.org image,
              apple-touch-icon, and in-page <img>; pick the
              highest-scoring candidate.

--candidates prints the ranked list as JSON without persisting, for
callers (e.g. the Mac picker sheet) that want to let the user choose.`,
	Args: cobra.ExactArgs(1),
	RunE: runThumbnailImport,
}

func init() {
	thumbnailSetCmd.Flags().String("file", "", "Local file to copy in as the thumbnail")
	thumbnailSetCmd.Flags().String("url", "", "Remote image URL to download")
	thumbnailImportCmd.Flags().String("from", "", "Source URL (defaults to item.url)")
	thumbnailImportCmd.Flags().Bool("candidates", false, "Print ranked candidates as JSON; do not persist")
	thumbnailBackfillCmd.Flags().Int("limit", 0, "Stop after N items (0 = no limit)")
	thumbnailBackfillCmd.Flags().Bool("dry-run", false, "List candidates without writing thumbnails")
	thumbnailBackfillCmd.Flags().Bool("images", false, "Backfill image items by downscaling their blobs (instead of URL items)")
	thumbnailCmd.AddCommand(thumbnailSetCmd)
	thumbnailCmd.AddCommand(thumbnailImportCmd)
	thumbnailCmd.AddCommand(thumbnailClearCmd)
	thumbnailCmd.AddCommand(thumbnailPathCmd)
	thumbnailCmd.AddCommand(thumbnailBackfillCmd)
	rootCmd.AddCommand(thumbnailCmd)
}

func runThumbnailBackfill(cmd *cobra.Command, _ []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	dry, _ := cmd.Flags().GetBool("dry-run")
	images, _ := cmd.Flags().GetBool("images")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	filterType := model.TypeURL
	label := "URL"
	if images {
		filterType = model.TypeImage
		label = "image"
	}

	// Pull items of the chosen type. ListItems' filter doesn't expose
	// thumbnail_path so we scan in memory; even a few thousand rows
	// is trivial.
	items, err := s.ListItems(ctx, model.ItemFilter{
		Type:  filterType,
		Limit: 0,
	})
	if err != nil {
		return fmt.Errorf("list items: %w", err)
	}

	var todo []model.Item
	for _, it := range items {
		if it.ThumbnailPath != "" {
			continue
		}
		if images {
			if it.StorePath != "" {
				todo = append(todo, it)
			}
		} else if it.URL != "" {
			todo = append(todo, it)
		}
	}
	if len(todo) == 0 {
		fmt.Printf("No %s items missing thumbnails.\n", label)
		return nil
	}
	if limit > 0 && len(todo) > limit {
		todo = todo[:limit]
	}

	fmt.Printf("Backfilling thumbnails for %d %s item%s%s\n",
		len(todo),
		label,
		map[bool]string{true: "", false: "s"}[len(todo) == 1],
		map[bool]string{true: " (dry run)", false: ""}[dry],
	)

	ok, failed := 0, 0
	fs := openFileStore()
	for i := range todo {
		it := &todo[i]
		hint := it.URL
		if images {
			hint = it.SourcePath
		}
		fmt.Printf("  [%d/%d] %s %s …", i+1, len(todo), shortID(it.ID), truncate(hint, 60))
		if dry {
			fmt.Println(" (skipped)")
			continue
		}
		var importErr error
		if images {
			_, importErr = thumbsync.ImportImageThumbnail(ctx, s, fs, it)
		} else {
			_, importErr = thumbsync.ImportForItem(ctx, s, fs, it, it.URL)
		}
		if importErr != nil {
			fmt.Printf(" failed (%s)\n", importErr.Error())
			failed++
			continue
		}
		fmt.Println(" ok")
		ok++
	}
	fmt.Printf("Done. %d succeeded, %d failed.\n", ok, failed)
	return nil
}

func runThumbnailSet(cmd *cobra.Command, args []string) error {
	filePath, _ := cmd.Flags().GetString("file")
	urlStr, _ := cmd.Flags().GetString("url")
	if filePath == "" && urlStr == "" {
		return fmt.Errorf("--file or --url required")
	}
	if filePath != "" && urlStr != "" {
		return fmt.Errorf("--file and --url are mutually exclusive")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}

	fs := openFileStore()
	thumbsDir := filepath.Join(fs.BaseDir(), "thumbnails")
	if err := os.MkdirAll(thumbsDir, 0755); err != nil {
		return fmt.Errorf("create thumbnails dir: %w", err)
	}

	var srcReader io.ReadCloser
	var ext string
	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open %s: %w", filePath, err)
		}
		srcReader = f
		ext = strings.ToLower(filepath.Ext(filePath))
	} else {
		ext, srcReader, err = thumbsync.DownloadImage(urlStr)
		if err != nil {
			return err
		}
	}
	defer srcReader.Close()

	if ext == "" {
		ext = ".jpg"
	}

	// Remove any previous thumbnail file for this item before writing
	// the new one — the extension may differ.
	if item.ThumbnailPath != "" {
		fs.RemoveRelative(item.ThumbnailPath)
	}

	relPath := filepath.Join("thumbnails", item.ID+ext)
	dest := filepath.Join(fs.BaseDir(), relPath)
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create thumbnail file: %w", err)
	}
	if _, err := io.Copy(out, srcReader); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("write thumbnail: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return fmt.Errorf("close thumbnail: %w", err)
	}

	item.ThumbnailPath = relPath
	if err := s.UpdateItem(ctx, item); err != nil {
		os.Remove(dest)
		return fmt.Errorf("update item: %w", err)
	}

	if flagJSON {
		printJSON(map[string]string{
			"id":             item.ID,
			"thumbnail_path": relPath,
			"abs_path":       dest,
		})
	} else {
		fmt.Printf("Set thumbnail for [%s] %s\n", shortID(item.ID), dest)
	}
	return nil
}

func runThumbnailClear(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	if item.ThumbnailPath == "" {
		if flagJSON {
			printJSON(map[string]string{"id": item.ID, "thumbnail_path": ""})
		} else {
			fmt.Printf("[%s] no thumbnail set\n", shortID(item.ID))
		}
		return nil
	}

	fs := openFileStore()
	if err := fs.RemoveRelative(item.ThumbnailPath); err != nil {
		return err
	}
	item.ThumbnailPath = ""
	if err := s.UpdateItem(ctx, item); err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	if flagJSON {
		printJSON(map[string]string{"id": item.ID, "thumbnail_path": ""})
	} else {
		fmt.Printf("Cleared thumbnail for [%s]\n", shortID(item.ID))
	}
	return nil
}

func runThumbnailPath(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	if item.ThumbnailPath == "" {
		return nil
	}
	fs := openFileStore()
	fmt.Println(fs.ResolveRelative(item.ThumbnailPath))
	return nil
}

// runThumbnailImport implements `stash thumbnail import` — the
// URL-driven harvest path used by the Mac app's "Import Thumbnail"
// flow and the rule engine's `set_thumbnail: { from: ... }` action.
func runThumbnailImport(cmd *cobra.Command, args []string) error {
	fromURL, _ := cmd.Flags().GetString("from")
	candidatesOnly, _ := cmd.Flags().GetBool("candidates")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}

	if fromURL == "" {
		fromURL = item.URL
	}
	if fromURL == "" {
		return fmt.Errorf("no source URL: pass --from or use a URL-typed item")
	}

	if candidatesOnly {
		return runThumbnailCandidates(item, fromURL)
	}
	result, err := thumbsync.ImportForItem(ctx, s, openFileStore(), item, fromURL)
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{
			"id":             item.ID,
			"thumbnail_path": result.RelPath,
			"source":         result.Source,
			"candidate_url":  result.CandidateURL,
		})
	} else {
		if result.Source == "direct" {
			fmt.Printf("Imported thumbnail for [%s]\n", shortID(item.ID))
		} else {
			fmt.Printf("Imported thumbnail for [%s] from %s (%s)\n",
				shortID(item.ID), result.CandidateURL, result.Source)
		}
	}
	return nil
}

func runThumbnailCandidates(item *model.Item, fromURL string) error {
	body, ct, err := thumbsync.FetchHTTP(fromURL, "")
	if err != nil {
		return err
	}
	defer body.Close()
	if strings.HasPrefix(strings.ToLower(ct), "image/") {
		printJSON([]extract.ThumbnailCandidate{
			{URL: fromURL, Source: "direct", Score: 1},
		})
		return nil
	}
	if !strings.Contains(strings.ToLower(ct), "html") {
		return fmt.Errorf("unsupported content-type %q", ct)
	}
	htmlBytes, err := io.ReadAll(io.LimitReader(body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read html: %w", err)
	}
	cands, err := extract.ExtractThumbnailCandidates(bytes.NewReader(htmlBytes), fromURL)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	printJSONSlice(cands)
	return nil
}

// (ThumbnailImportResult / importThumbnailForItem / writeThumbnail
// moved into internal/thumbsync.)
