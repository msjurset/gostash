package main

import (
	"context"
	"fmt"

	"github.com/msjurset/gostash/internal/audit"
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
				continue
			}
			logTagAudit(&item, audit.ActionAdd, t, "bulk")
		}

		// Trigger rules if any tag was added. This ensures rules matching on
		// `has_tag` (like voice-to-journal) fire when the user tags an item
		// from the Mac app or CLI.
		if !failed && len(addTags) > 0 {
			full, err := s.GetItem(ctx, item.ID)
			if err == nil {
				res := ApplyRulesToItem(s, full, RuleApplyContext{})
				// Re-save core item fields if rules updated them (e.g. title/note)
				if res.Title != "" || res.HasNoteUpdate() {
					if err := s.UpdateItem(ctx, full); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: [%s] failed to update item from rules: %v\n", shortID(item.ID), err)
					}
				}
				FirePostSaveRuleEffects(ctx, s, full, res)
			}
		}

		for _, t := range rmTags {
			if err := s.RemoveTag(ctx, item.ID, t); err != nil {
				errs = append(errs, fmt.Errorf("[%s] remove tag %q: %w", shortID(item.ID), t, err))
				failed = true
				continue
			}
			logTagAudit(&item, audit.ActionRemove, t, "bulk")
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
