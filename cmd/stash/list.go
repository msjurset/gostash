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
	listCmd.Flags().String("collection", "", "Filter by collection")
	listCmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	listCmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	listCmd.Flags().IntP("limit", "l", 50, "Max results")
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
