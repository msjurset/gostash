package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var healCmd = &cobra.Command{
	Use:   "heal [id]",
	Short: "Re-fetch a missing content blob from its source URL",
	Long: `Re-download an item's content from its stored source URL when the
on-disk blob has gone missing. Validates the new bytes' SHA-256 against
the recorded content_hash:

  - hash matches: drop the bytes back at the expected path, item works
    again, no DB changes.
  - hash differs (CDN re-encoded since capture, etc.): update the item's
    store_path, content_hash, file_size, and mime_type to point at the
    new bytes.

Only image and file items are eligible — URL/snippet items have no
blob to restore. With --all, scans every eligible item and heals any
whose blob is missing.

Examples:
  stash heal 01KR9HKVDG          # heal one item
  stash heal --all               # heal every missing-blob item
  stash heal --all --dry-run     # report what would be healed`,
	RunE: runHeal,
}

func init() {
	healCmd.Flags().Bool("all", false, "Scan every eligible item and heal any whose blob is missing")
	healCmd.Flags().Bool("dry-run", false, "Report what would be healed without fetching")
	healCmd.Flags().Bool("force", false, "Re-fetch even if the blob is already present on disk")
	rootCmd.AddCommand(healCmd)
}

func runHeal(cmd *cobra.Command, args []string) error {
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

	targets, err := healTargets(ctx, s, fs, args, all, force)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if flagJSON {
			printJSON(map[string]any{"healed": 0, "skipped": 0, "errors": 0})
		} else {
			fmt.Println("Nothing to heal.")
		}
		return nil
	}

	if dryRun {
		if flagJSON {
			out := make([]map[string]string, 0, len(targets))
			for _, t := range targets {
				out = append(out, map[string]string{"id": t.ID, "url": t.URL, "title": t.Title})
			}
			printJSON(map[string]any{"would_heal": out})
		} else {
			fmt.Printf("Would heal %d item(s):\n", len(targets))
			for _, t := range targets {
				fmt.Printf("  [%s] %s — %s\n", shortID(t.ID), t.Title, t.URL)
			}
		}
		return nil
	}

	healed := 0
	rehashed := 0
	var errs []string
	for _, item := range targets {
		outcome, err := healOne(ctx, s, fs, item)
		if err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", shortID(item.ID), err))
			if !flagJSON {
				fmt.Printf("  ✗ [%s] %s — %v\n", shortID(item.ID), item.Title, err)
			}
			continue
		}
		healed++
		if outcome == healOutcomeRehashed {
			rehashed++
		}
		if !flagJSON {
			tag := "matched"
			if outcome == healOutcomeRehashed {
				tag = "rehashed"
			}
			fmt.Printf("  ✓ [%s] %s (%s)\n", shortID(item.ID), item.Title, tag)
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"healed":   healed,
			"rehashed": rehashed,
			"errors":   len(errs),
		})
	} else {
		fmt.Printf("\nHealed %d item(s) (%d rehashed, %d error(s))\n", healed, rehashed, len(errs))
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s)", len(errs))
	}
	return nil
}

// healTargets resolves the set of items the heal command should
// operate on. Either a single id from args or every eligible item
// when --all is set. Items are filtered to image/file types with a
// non-empty source URL and (unless --force) a missing on-disk blob.
func healTargets(ctx context.Context, s store.Store, fs *filestore.FileStore, args []string, all, force bool) ([]model.Item, error) {
	if !all {
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return nil, err
		}
		if !healEligible(*item) {
			return nil, fmt.Errorf("item is not heal-eligible (need type image or file with a URL, got type=%s url=%q)", item.Type.Display(), item.URL)
		}
		if !force && fs.Exists(item.ContentHash) {
			return nil, fmt.Errorf("blob already present on disk; pass --force to re-fetch anyway")
		}
		return []model.Item{*item}, nil
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var targets []model.Item
	for _, item := range items {
		if !healEligible(item) {
			continue
		}
		if !force && fs.Exists(item.ContentHash) {
			continue
		}
		targets = append(targets, item)
	}
	return targets, nil
}

func healEligible(item model.Item) bool {
	if item.Type != model.TypeImage && item.Type != model.TypeFile {
		return false
	}
	if item.URL == "" || item.ContentHash == "" {
		return false
	}
	return true
}

type healOutcome int

const (
	healOutcomeMatched healOutcome = iota
	healOutcomeRehashed
)

// healOne does a single fetch + save. Returns matched when the new
// bytes' hash equals the recorded content_hash (no DB change needed),
// rehashed when the bytes differ and the item record was updated to
// point at the new hash.
func healOne(ctx context.Context, s store.Store, fs *filestore.FileStore, item model.Item) (healOutcome, error) {
	body, ctype, _, err := fetchURLBytes(item.URL, "")
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}
	hash, size, err := fs.Save(bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	if hash == item.ContentHash {
		return healOutcomeMatched, nil
	}
	// Hash drifted (CDN re-encoded, etc.). Update the record to
	// point at the new bytes so the surviving item resolves cleanly.
	// MIME from response header overrides anything stale; we keep
	// the previous value when the header is empty.
	item.ContentHash = hash
	item.StorePath = hash
	item.FileSize = size
	if mt := strings.ToLower(strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0])); mt != "" {
		item.MimeType = mt
	}
	if err := s.UpdateItem(ctx, &item); err != nil {
		return 0, fmt.Errorf("update: %w", err)
	}
	return healOutcomeRehashed, nil
}
