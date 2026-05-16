package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var bulkDeleteCmd = &cobra.Command{
	Use:   "delete [ids...]",
	Short: "Delete multiple items",
	RunE:  runBulkDelete,
}

func init() {
	bulkDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	addBulkFilterFlags(bulkDeleteCmd)
	bulkCmd.AddCommand(bulkDeleteCmd)
}

func runBulkDelete(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	items, err := resolveItems(cmd, args, s)
	if err != nil {
		return err
	}

	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		fmt.Printf("Items to delete (%d):\n", len(items))
		printItems(items)
		fmt.Printf("\nDelete all %d items? (y/N) ", len(items))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	ctx := context.Background()
	fs := openFileStore()
	var errs []error
	ok := 0

	for _, item := range items {
		if err := s.DeleteItem(ctx, item.ID); err != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", shortID(item.ID), err))
			continue
		}
		if item.ThumbnailPath != "" {
			fs.RemoveRelative(item.ThumbnailPath)
		}
		// Refcount the shared blob before yanking the file —
		// otherwise deleting one of two duplicates leaves the
		// surviving record with a dangling store_path. Counted
		// AFTER the row is deleted so this item's own row doesn't
		// count toward "still referenced."
		if item.ContentHash != "" {
			refs, err := s.CountItemsByContentHash(ctx, item.ContentHash)
			if err == nil && refs == 0 {
				fs.Delete(item.ContentHash)
			}
		}
		ok++
		if !flagJSON {
			fmt.Printf("  Deleted [%s] %s\n", shortID(item.ID), item.Title)
		}
	}

	if flagJSON {
		printJSON(map[string]any{"deleted": ok, "errors": len(errs)})
	} else {
		fmt.Printf("Deleted %d items\n", ok)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", e)
		}
		return fmt.Errorf("%d errors occurred", len(errs))
	}
	return nil
}
