package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var bulkCollectCmd = &cobra.Command{
	Use:   "collect [ids...]",
	Short: "Add or remove items from a collection",
	RunE:  runBulkCollect,
}

func init() {
	bulkCollectCmd.Flags().StringP("collection", "c", "", "Target collection (required)")
	bulkCollectCmd.MarkFlagRequired("collection")
	bulkCollectCmd.Flags().Bool("remove", false, "Remove from collection instead of adding")
	addBulkFilterFlags(bulkCollectCmd)
	bulkCmd.AddCommand(bulkCollectCmd)
}

func runBulkCollect(cmd *cobra.Command, args []string) error {
	col, _ := cmd.Flags().GetString("collection")
	remove, _ := cmd.Flags().GetBool("remove")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	items, err := resolveItems(cmd, args, s)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var errs []error
	ok := 0

	for _, item := range items {
		var opErr error
		if remove {
			opErr = s.RemoveFromCollection(ctx, item.ID, col)
		} else {
			opErr = s.AddToCollection(ctx, item.ID, col)
		}
		if opErr != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", shortID(item.ID), opErr))
			continue
		}
		ok++
		if !flagJSON {
			verb := "added to"
			if remove {
				verb = "removed from"
			}
			fmt.Printf("  [%s] %s %s %s\n", shortID(item.ID), item.Title, verb, col)
		}
	}

	if flagJSON {
		action := "added"
		if remove {
			action = "removed"
		}
		printJSON(map[string]any{"collection": col, "action": action, "count": ok, "errors": len(errs)})
	} else {
		verb := "Added"
		prep := "to"
		if remove {
			verb = "Removed"
			prep = "from"
		}
		fmt.Printf("%s %d items %s %q\n", verb, ok, prep, col)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", e)
		}
		return fmt.Errorf("%d errors occurred", len(errs))
	}
	return nil
}
