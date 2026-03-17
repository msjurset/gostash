package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a stashed item",
	Args:  cobra.ExactArgs(1),
	RunE:  runEdit,
}

func init() {
	editCmd.Flags().StringP("title", "t", "", "New title")
	editCmd.Flags().StringP("note", "n", "", "New note")
	editCmd.Flags().StringSlice("add-tag", nil, "Add tags (repeatable)")
	editCmd.Flags().StringSlice("remove-tag", nil, "Remove tags (repeatable)")
	editCmd.Flags().StringP("collection", "c", "", "Add to collection")
	rootCmd.AddCommand(editCmd)
}

func runEdit(cmd *cobra.Command, args []string) error {
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

	if cmd.Flags().Changed("title") {
		item.Title, _ = cmd.Flags().GetString("title")
	}
	if cmd.Flags().Changed("note") {
		item.Notes, _ = cmd.Flags().GetString("note")
	}

	if err := s.UpdateItem(ctx, item); err != nil {
		return err
	}

	// Handle tag additions
	if addTags, _ := cmd.Flags().GetStringSlice("add-tag"); len(addTags) > 0 {
		for _, t := range addTags {
			if err := s.AddTag(ctx, id, t); err != nil {
				return fmt.Errorf("add tag %q: %w", t, err)
			}
		}
	}

	// Handle tag removals
	if rmTags, _ := cmd.Flags().GetStringSlice("remove-tag"); len(rmTags) > 0 {
		for _, t := range rmTags {
			if err := s.RemoveTag(ctx, id, t); err != nil {
				return fmt.Errorf("remove tag %q: %w", t, err)
			}
		}
	}

	// Handle collection
	if col, _ := cmd.Flags().GetString("collection"); col != "" {
		if err := s.AddToCollection(ctx, id, col); err != nil {
			return fmt.Errorf("add to collection: %w", err)
		}
	}

	// Re-fetch to show updated state
	item, err = s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	if flagJSON {
		printJSON(item)
	} else {
		fmt.Printf("Updated [%s] %s\n", shortID(item.ID), item.Title)
	}
	return nil
}
