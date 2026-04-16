package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var bulkTagCmd = &cobra.Command{
	Use:   "tag [ids...]",
	Short: "Add or remove tags on multiple items",
	RunE:  runBulkTag,
}

func init() {
	bulkTagCmd.Flags().StringSlice("add-tag", nil, "Tags to add (repeatable)")
	bulkTagCmd.Flags().StringSlice("remove-tag", nil, "Tags to remove (repeatable)")
	addBulkFilterFlags(bulkTagCmd)
	bulkCmd.AddCommand(bulkTagCmd)
}

func runBulkTag(cmd *cobra.Command, args []string) error {
	addTags, _ := cmd.Flags().GetStringSlice("add-tag")
	rmTags, _ := cmd.Flags().GetStringSlice("remove-tag")
	if len(addTags) == 0 && len(rmTags) == 0 {
		return fmt.Errorf("specify --add-tag or --remove-tag")
	}

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
		failed := false
		for _, t := range addTags {
			if err := s.AddTag(ctx, item.ID, t); err != nil {
				errs = append(errs, fmt.Errorf("[%s] add tag %q: %w", shortID(item.ID), t, err))
				failed = true
			}
		}
		for _, t := range rmTags {
			if err := s.RemoveTag(ctx, item.ID, t); err != nil {
				errs = append(errs, fmt.Errorf("[%s] remove tag %q: %w", shortID(item.ID), t, err))
				failed = true
			}
		}
		if !failed {
			ok++
			if !flagJSON {
				fmt.Printf("  [%s] %s\n", shortID(item.ID), item.Title)
			}
		}
	}

	if flagJSON {
		printJSON(map[string]any{"updated": ok, "errors": len(errs)})
	} else {
		fmt.Printf("Updated %d items\n", ok)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", e)
		}
		return fmt.Errorf("%d errors occurred", len(errs))
	}
	return nil
}
