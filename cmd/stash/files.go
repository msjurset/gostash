package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var filesCmd = &cobra.Command{
	Use:   "files <id>",
	Short: "List files attached to an item",
	Long: `Shows the primary file (items.store_path) at index 0 followed
by every row in the item_files sidecar in carousel order. Useful
for finding the file index you need to pass to 'stash detach',
'stash reorder', or 'stash primary'.`,
	Args: cobra.ExactArgs(1),
	RunE: runFiles,
}

var detachCmd = &cobra.Command{
	Use:   "detach <id> <file-index>",
	Short: "Remove an attached file from an item",
	Long: `Removes the file at <file-index> from item_files. Index 0 is
the primary (items.store_path) and cannot be detached this way —
use 'stash primary' to promote a different file first.

The content-addressed blob is left in place: refcount-based GC is
a separate operation (see 'stash check' / future 'stash gc').`,
	Args: cobra.ExactArgs(2),
	RunE: runDetach,
}

var primaryCmd = &cobra.Command{
	Use:   "primary <id> <file-index>",
	Short: "Promote an attached file to be the item's primary",
	Long: `Swaps the file at <file-index> with the primary (items.store_path).
The previous primary is moved into item_files at position 0 so
nothing is lost — only the cover changes.`,
	Args: cobra.ExactArgs(2),
	RunE: runPrimary,
}

var reorderCmd = &cobra.Command{
	Use:   "reorder <id> <index> [<index>...]",
	Short: "Reorder the carousel of attached files",
	Long: `Rewrites the position of every attached file (excluding the
primary at index 0) to match the new ordering. The list must
contain exactly the set of attached-file indices, just in a new
order.

Example — three attached files, swap last two:
  stash reorder 01KR9HKVDG 1 3 2`,
	Args: cobra.MinimumNArgs(2),
	RunE: runReorder,
}

func init() {
	rootCmd.AddCommand(filesCmd)
	rootCmd.AddCommand(detachCmd)
	rootCmd.AddCommand(primaryCmd)
	rootCmd.AddCommand(reorderCmd)
}

func runFiles(cmd *cobra.Command, args []string) error {
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
	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]any{
			"primary": map[string]any{
				"store_path":   item.StorePath,
				"content_hash": item.ContentHash,
				"mime_type":    item.MimeType,
				"file_size":    item.FileSize,
			},
			"attached": files,
		})
		return nil
	}
	fmt.Printf("Files for [%s] %s\n", shortID(item.ID), item.Title)
	fmt.Printf("  0  primary       %s (%s, %d bytes)\n",
		shortHash(item.ContentHash), item.MimeType, item.FileSize)
	for i, f := range files {
		cap := f.Caption
		if cap == "" {
			cap = "(no caption)"
		}
		fmt.Printf("  %d  attached  #%d  %s (%s, %d bytes) — %s\n",
			i+1, f.ID, shortHash(f.ContentHash), f.MimeType, f.FileSize, cap)
	}
	return nil
}

func runDetach(cmd *cobra.Command, args []string) error {
	idx, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("file-index must be an integer: %w", err)
	}
	if idx == 0 {
		return fmt.Errorf("index 0 is the primary; use 'stash primary' to swap covers first")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Resolve prefix → full id first; ListItemFiles matches by
	// item_id directly.
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		return err
	}
	if idx-1 < 0 || idx-1 >= len(files) {
		return fmt.Errorf("file-index %d out of range (item has %d attached file(s))", idx, len(files))
	}
	target := files[idx-1]
	if err := s.DetachItemFile(ctx, target.ID); err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{"detached": target.ID})
	} else {
		fmt.Printf("Detached file #%d (%s)\n", target.ID, shortHash(target.ContentHash))
	}
	return nil
}

func runPrimary(cmd *cobra.Command, args []string) error {
	idx, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("file-index must be an integer: %w", err)
	}
	if idx == 0 {
		return fmt.Errorf("index 0 is already the primary")
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
	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		return err
	}
	if idx-1 < 0 || idx-1 >= len(files) {
		return fmt.Errorf("file-index %d out of range (item has %d attached file(s))", idx, len(files))
	}
	target := files[idx-1]
	if err := s.PromoteItemFile(ctx, target.ID); err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{"promoted": target.ID})
	} else {
		fmt.Printf("Promoted attached file #%d to primary\n", target.ID)
	}
	return nil
}

func runReorder(cmd *cobra.Command, args []string) error {
	rawIdx := args[1:]
	indices := make([]int, len(rawIdx))
	for i, s := range rawIdx {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("index must be an integer: %q", s)
		}
		if v <= 0 {
			return fmt.Errorf("reorder operates on attached files only (indices 1+, not 0/primary)")
		}
		indices[i] = v
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
	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		return err
	}
	if len(indices) != len(files) {
		return fmt.Errorf("reorder expects %d indices, got %d", len(files), len(indices))
	}
	orderedIDs := make([]int64, len(indices))
	for i, idx := range indices {
		if idx-1 < 0 || idx-1 >= len(files) {
			return fmt.Errorf("index %d out of range", idx)
		}
		orderedIDs[i] = files[idx-1].ID
	}
	if err := s.ReorderItemFiles(ctx, item.ID, orderedIDs); err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{"reordered": orderedIDs})
	} else {
		fmt.Printf("Reordered %d attached file(s).\n", len(orderedIDs))
	}
	return nil
}

func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10]
	}
	return h
}
