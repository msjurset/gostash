package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a stashed item",
	Args:  cobra.ExactArgs(1),
	RunE:  runDelete,
}

func init() {
	deleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	rootCmd.AddCommand(deleteCmd)
}

func runDelete(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	id := args[0]

	item, err := s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		fmt.Printf("Delete %s [%s] %s? (y/N) ", item.Type, shortID(item.ID), item.Title)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := s.DeleteItem(ctx, id); err != nil {
		return err
	}

	// Thumbnail is per-item (`thumbnails/<id>.jpg`), safe to remove
	// unconditionally. The content blob is hash-addressed and can be
	// shared by duplicate items — only remove it when no other row
	// references the same content_hash, otherwise the surviving
	// sibling ends up with a dangling pointer.
	if item.ContentHash != "" || item.ThumbnailPath != "" {
		fs := openFileStore()
		if item.ThumbnailPath != "" {
			fs.RemoveRelative(item.ThumbnailPath)
		}
		if item.ContentHash != "" {
			refs, err := s.CountItemsByContentHash(ctx, item.ContentHash)
			if err == nil && refs == 0 {
				fs.Delete(item.ContentHash)
			}
		}
	}

	if flagJSON {
		printJSON(map[string]string{"deleted": id})
	} else {
		fmt.Printf("Deleted %s [%s]\n", item.Type, shortID(item.ID))
	}
	return nil
}
