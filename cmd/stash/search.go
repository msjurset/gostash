package main

import (
	"context"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Full-text search across all stashed items",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	searchCmd.Flags().StringSlice("tag", nil, "Filter by tag (repeatable)")
	searchCmd.Flags().String("collection", "", "Filter by collection")
	searchCmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	searchCmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	searchCmd.Flags().IntP("limit", "l", 50, "Max results")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	filter, err := buildFilter(cmd, args[0])
	if err != nil {
		return err
	}

	items, err := s.SearchItems(context.Background(), filter)
	if err != nil {
		return err
	}

	printItems(items)
	return nil
}

func buildFilter(cmd *cobra.Command, query string) (model.ItemFilter, error) {
	f := model.ItemFilter{Query: query}

	if v, _ := cmd.Flags().GetString("type"); v != "" {
		f.Type = model.ParseItemType(v)
	}
	if v, _ := cmd.Flags().GetStringSlice("tag"); len(v) > 0 {
		f.Tags = v
	}
	if v, _ := cmd.Flags().GetString("collection"); v != "" {
		f.Collection = v
	}
	if v, _ := cmd.Flags().GetString("after"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return f, err
		}
		f.After = &t
	}
	if v, _ := cmd.Flags().GetString("before"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return f, err
		}
		f.Before = &t
	}
	f.Limit, _ = cmd.Flags().GetInt("limit")
	return f, nil
}
