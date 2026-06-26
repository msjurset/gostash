package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:   "attach <id> <file> [--caption \"...\"]",
	Short: "Attach an additional file to an existing item",
	Long: `Add a file to an existing item as an "attached photo" — used
when one logical subject (a mushroom, a bird) is best captured from
several angles or as several states (male/female, top/side/bottom).

The item's primary file (items.store_path) is unchanged; the new
file lands in the item_files sidecar table and appears in the Mac
detail-pane carousel + Android pager.

Examples:
  stash attach 01KR9HKVDG ~/Pictures/mushroom-side.jpg
  stash attach 01KR9HKVDG ~/Pictures/female.jpg --caption "female"`,
	Args: cobra.ExactArgs(2),
	RunE: runAttach,
}

func init() {
	attachCmd.Flags().String("caption", "", "Optional caption for the attached file (\"side view\", \"male\", etc)")
	rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, args []string) error {
	id := args[0]
	path := args[1]
	caption, _ := cmd.Flags().GetString("caption")

	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("attach takes a file, not a directory")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	item, err := s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	f, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	hash, size, err := fs.Save(f)
	if err != nil {
		return fmt.Errorf("store blob: %w", err)
	}

	// Detect MIME the same way captures do.
	mime, err := sniffMIME(abs)
	if err != nil {
		// Non-fatal — we can still attach without a clean MIME.
		mime = "application/octet-stream"
	}

	file := &model.ItemFile{
		ItemID:      item.ID,
		StorePath:   hash,
		ContentHash: hash,
		MimeType:    mime,
		FileSize:    size,
		Caption:     caption,
	}
	if err := s.AttachItemFile(ctx, file); err != nil {
		return err
	}

	if flagJSON {
		printJSON(file)
	} else {
		fmt.Printf("Attached file #%d to [%s] %s (%s, %d bytes)\n",
			file.ID, shortID(item.ID), item.Title, mime, size)
	}
	return nil
}

func sniffMIME(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return extract.DetectMIME(head[:n], filepath.Base(path)), nil
}
