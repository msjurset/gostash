package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var backfillLocationsCmd = &cobra.Command{
	Use:   "backfill-locations [id]",
	Short: "Extract GPS from image EXIF and write it to item.location",
	Long: `Walk image items, read each blob's JPEG EXIF, and fill the
location field when GPS tags are present. Idempotent — items that
already have a location are skipped unless --force is set, in which
case existing EXIF-sourced locations are overwritten with a fresh
read (manual / capture sources are left alone).

Items without GPS in their EXIF are silently skipped — most non-
phone images simply don't carry a fix. HEIC and other container
formats can't be decoded by the underlying library and are also
skipped.

Examples:
  stash backfill-locations 01KR9HKVDG    # one item
  stash backfill-locations --all          # every missing-location image
  stash backfill-locations --all --dry-run
  stash backfill-locations --all --force  # re-extract EXIF over existing rows`,
	RunE: runBackfillLocations,
}

func init() {
	backfillLocationsCmd.Flags().Bool("all", false, "Process every image item that has no location yet")
	backfillLocationsCmd.Flags().Bool("dry-run", false, "Report what would change without writing")
	backfillLocationsCmd.Flags().Bool("force", false, "Re-extract EXIF and overwrite existing exif-sourced locations (manual/capture sources preserved)")
	rootCmd.AddCommand(backfillLocationsCmd)
}

func runBackfillLocations(cmd *cobra.Command, args []string) error {
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

	targets, err := backfillTargets(ctx, s, args, all, force)
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
				out = append(out, map[string]string{"id": t.ID, "title": t.Title})
			}
			printJSON(map[string]any{"would_process": out})
		} else {
			fmt.Printf("Would process %d image item(s) for EXIF GPS.\n", len(targets))
			for _, t := range targets {
				fmt.Printf("  [%s] %s\n", shortID(t.ID), t.Title)
			}
		}
		return nil
	}

	updated := 0
	skipped := 0
	var errs []string
	for _, item := range targets {
		outcome, err := backfillOne(ctx, s, fs, item)
		if err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", shortID(item.ID), err))
			if !flagJSON {
				fmt.Printf("  ✗ [%s] %s — %v\n", shortID(item.ID), item.Title, err)
			}
			continue
		}
		switch outcome.kind {
		case backfillKindUpdated:
			updated++
			if !flagJSON {
				fmt.Printf("  ✓ [%s] %s → %.6f, %.6f\n", shortID(item.ID), item.Title, outcome.lat, outcome.lon)
			}
		case backfillKindNoGPS:
			skipped++
			if !flagJSON {
				fmt.Printf("  - [%s] %s — no GPS\n", shortID(item.ID), item.Title)
			}
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"updated": updated,
			"skipped": skipped,
			"errors":  len(errs),
		})
	} else {
		fmt.Printf("\nUpdated %d, skipped %d (no GPS), %d error(s).\n", updated, skipped, len(errs))
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s)", len(errs))
	}
	return nil
}

// backfillTargets resolves the set of image items the backfill
// command should operate on. Single id or every eligible item.
// "Eligible" means image type with a content hash on disk and,
// unless --force is set, no existing location.
func backfillTargets(ctx context.Context, s store.Store, args []string, all, force bool) ([]model.Item, error) {
	if !all {
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return nil, err
		}
		if item.Type != model.TypeImage {
			return nil, fmt.Errorf("item type is %s, not image", item.Type.Display())
		}
		if item.ContentHash == "" {
			return nil, fmt.Errorf("item has no stored content hash")
		}
		if !force && item.Location != nil {
			return nil, fmt.Errorf("item already has a location (source=%q); pass --force to overwrite", item.Location.Source)
		}
		return []model.Item{*item}, nil
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var targets []model.Item
	for _, item := range items {
		if item.Type != model.TypeImage || item.ContentHash == "" {
			continue
		}
		if item.Location != nil && !force {
			continue
		}
		// Even with --force, preserve human-set / mobile-captured
		// locations — EXIF should only ever overwrite EXIF.
		if item.Location != nil && item.Location.Source != "exif" {
			continue
		}
		targets = append(targets, item)
	}
	return targets, nil
}

type backfillKind int

const (
	backfillKindUpdated backfillKind = iota
	backfillKindNoGPS
)

type backfillOutcome struct {
	kind     backfillKind
	lat, lon float64
}

func backfillOne(ctx context.Context, s store.Store, fs *filestore.FileStore, item model.Item) (backfillOutcome, error) {
	f, err := fs.Open(item.ContentHash)
	if err != nil {
		return backfillOutcome{}, fmt.Errorf("open blob: %w", err)
	}
	defer f.Close()

	lat, lon, gpsErr := exif.ExtractGPS(f)
	if gpsErr != nil {
		if errors.Is(gpsErr, exif.ErrNoGPS) {
			return backfillOutcome{kind: backfillKindNoGPS}, nil
		}
		// Non-GPS decode errors (truncated file, unsupported format,
		// etc.) also reduce to "no usable GPS" — the backfill is
		// best-effort and skipping is the correct behaviour.
		return backfillOutcome{kind: backfillKindNoGPS}, nil
	}

	item.Location = &model.Location{Lat: lat, Lon: lon, Source: "exif"}
	if err := s.UpdateItem(ctx, &item); err != nil {
		return backfillOutcome{}, fmt.Errorf("update: %w", err)
	}
	return backfillOutcome{kind: backfillKindUpdated, lat: lat, lon: lon}, nil
}
