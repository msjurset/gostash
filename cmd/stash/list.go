package main

import (
	"context"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List stashed items",
	RunE:  runList,
}

func init() {
	listCmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	listCmd.Flags().StringSlice("tag", nil, "Filter by tag (repeatable)")
	listCmd.Flags().StringSlice("exclude-tag", nil, "Exclude items carrying any of these tags (repeatable)")
	listCmd.Flags().Bool("untagged", false, "Only items with no tags")
	listCmd.Flags().String("collection", "", "Filter by collection")
	listCmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	listCmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	listCmd.Flags().String("recent", "", "Only items captured within this duration (e.g. 7d, 2w, 6h)")
	listCmd.Flags().String("regex", "", "RE2 pattern matched against title+notes+url+extracted text. Prefix with `!` to negate.")
	listCmd.Flags().IntP("limit", "l", 50, "Max results")
	listCmd.Flags().Bool("include-archived", false, "Also show archived items")
	listCmd.Flags().Bool("archived", false, "Show only archived items (overrides --include-archived)")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	filter, err := buildFilter(cmd, "")
	if err != nil {
		return err
	}

	items, err := s.ListItems(context.Background(), filter)
	if err != nil {
		return err
	}

	printItems(items)
	return nil
}
