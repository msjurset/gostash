package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var backfillCameraCmd = &cobra.Command{
	Use:   "backfill-camera-exif [id]",
	Short: "Fill items.metadata.camera from EXIF Make/Model/aperture/ISO/etc.",
	Long: `Walk image items and extract camera-info EXIF (Make, Model,
LensModel, FNumber, ExposureTime, FocalLength, ISOSpeedRatings,
PixelXDimension / PixelYDimension) into items.metadata.camera.
Backs the Mac detail view's "Capture device" row.

Idempotent — items that already have a populated camera object
are skipped unless --force is passed (which re-extracts and
overwrites). Items where EXIF can't be decoded (HEIC, screenshots,
share-stripped JPEGs) are silently skipped.

Examples:
  stash backfill-camera-exif 01KR9HKVDG   # one item
  stash backfill-camera-exif --all
  stash backfill-camera-exif --all --dry-run
  stash backfill-camera-exif --all --force`,
	RunE: runBackfillCamera,
}

func init() {
	backfillCameraCmd.Flags().Bool("all", false, "Process every image item without camera metadata")
	backfillCameraCmd.Flags().Bool("dry-run", false, "Report what would change without writing")
	backfillCameraCmd.Flags().Bool("force", false, "Re-extract and overwrite existing camera metadata")
	rootCmd.AddCommand(backfillCameraCmd)
}

func runBackfillCamera(cmd *cobra.Command, args []string) error {
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

	targets, err := cameraTargets(ctx, s, args, all, force)
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
		if !flagJSON {
			fmt.Printf("Would process %d image item(s) for camera EXIF.\n", len(targets))
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
		cam, source, err := deriveCamera(fs, item)
		if err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", shortID(item.ID), err))
			continue
		}
		if !cam.HasAny() {
			skipped++
			if !flagJSON {
				fmt.Printf("  - [%s] %s — no camera EXIF\n", shortID(item.ID), item.Title)
			}
			continue
		}
		item.Metadata = mergeCameraIntoMetadata(item.Metadata, cam)
		if err := s.UpdateItem(ctx, &item); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] update: %v", shortID(item.ID), err))
			continue
		}
		updated++
		if !flagJSON {
			label := cam.Model
			if label == "" {
				label = cam.Make
			}
			fmt.Printf("  ✓ [%s] %s → %s (%s)\n", shortID(item.ID), item.Title, label, source)
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

// cameraTargets resolves which image items to process. URL /
// snippet / email types are excluded (no in-content EXIF) without
// surfacing an error per item — they're not the kind of thing the
// user would expect camera info from.
func cameraTargets(ctx context.Context, s store.Store, args []string, all, force bool) ([]model.Item, error) {
	if !all {
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return nil, err
		}
		if item.Type != model.TypeImage {
			return nil, fmt.Errorf("item type is %s, not image", item.Type.Display())
		}
		if !force && hasCameraMetadata(item.Metadata) {
			return nil, fmt.Errorf("item already has camera metadata; pass --force to overwrite")
		}
		return []model.Item{*item}, nil
	}
	items, err := s.ListItems(ctx, model.ItemFilter{
		Type:            model.TypeImage,
		IncludeArchived: true,
		Limit:           0,
	})
	if err != nil {
		return nil, err
	}
	var targets []model.Item
	for _, it := range items {
		if !force && hasCameraMetadata(it.Metadata) {
			continue
		}
		targets = append(targets, it)
	}
	return targets, nil
}

// deriveCamera walks the primary blob (and attached files, in
// case the primary was share-stripped) extracting EXIF camera
// info. Returns the first non-empty hit. The string return names
// which slot the info came from for verbose output.
func deriveCamera(fs *filestore.FileStore, item model.Item) (exif.Camera, string, error) {
	for i, hash := range append([]string{item.ContentHash}, attachedHashes(item)...) {
		if hash == "" {
			continue
		}
		f, err := fs.Open(hash)
		if err != nil {
			continue
		}
		cam, err := exif.ExtractCamera(f)
		f.Close()
		if err != nil {
			continue
		}
		if cam.HasAny() {
			if i == 0 {
				return cam, "primary", nil
			}
			return cam, fmt.Sprintf("attached#%d", i), nil
		}
	}
	return exif.Camera{}, "", nil
}

// hasCameraMetadata returns true when items.metadata already
// carries a populated "camera" key. Used by the idempotency check
// — items with existing camera info are skipped unless --force.
func hasCameraMetadata(meta json.RawMessage) bool {
	if len(meta) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		return false
	}
	cam, ok := m["camera"].(map[string]any)
	if !ok {
		return false
	}
	return len(cam) > 0
}

// mergeCameraIntoMetadata mirrors the helper in internal/stash and
// internal/server. Kept local to the cmd/stash package to avoid
// dragging the entire internal/stash dependency tree into a
// backfill command that only needs the merge primitive.
func mergeCameraIntoMetadata(existing json.RawMessage, cam exif.Camera) json.RawMessage {
	var m map[string]any
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &m)
	}
	if m == nil {
		m = make(map[string]any)
	}
	m["camera"] = cam
	out, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	return out
}

var _ = os.Stdout // keep `os` imported when no other reference survives a refactor
