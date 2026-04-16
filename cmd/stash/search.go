package main

import (
	"context"
	"fmt"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Full-text search across all stashed items",
	Long: `Full-text search across all stashed items.

  stash search golang             # search for "golang"
  stash search save fav-go --type url --tag go    # save a search
  stash search list               # list saved searches
  stash search run fav-go         # run a saved search
  stash search delete fav-go      # delete a saved search`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

var searchSaveCmd = &cobra.Command{
	Use:   "save <name> [query]",
	Short: "Save a search for later",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runSearchSave,
}

var searchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved searches",
	RunE:  runSearchList,
}

var searchRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Run a saved search",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchRun,
}

var searchDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a saved search",
	Args:  cobra.ExactArgs(1),
	RunE:  runSearchDelete,
}

func init() {
	addSearchFilterFlags(searchCmd)
	addSearchFilterFlags(searchSaveCmd)
	searchCmd.AddCommand(searchSaveCmd)
	searchCmd.AddCommand(searchListCmd)
	searchCmd.AddCommand(searchRunCmd)
	searchCmd.AddCommand(searchDeleteCmd)
	rootCmd.AddCommand(searchCmd)
}

func addSearchFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	cmd.Flags().StringSlice("tag", nil, "Filter by tag (repeatable)")
	cmd.Flags().String("collection", "", "Filter by collection")
	cmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	cmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	cmd.Flags().IntP("limit", "l", 50, "Max results")
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

func runSearchSave(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	name := args[0]
	query := ""
	if len(args) > 1 {
		query = args[1]
	}

	filter, err := buildFilter(cmd, query)
	if err != nil {
		return err
	}
	filter.Query = "" // stored separately

	if err := s.SaveSearch(context.Background(), name, query, filter); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]string{"saved": name})
	} else {
		fmt.Printf("Saved search %q\n", name)
	}
	return nil
}

func runSearchList(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	searches, err := s.ListSavedSearches(context.Background())
	if err != nil {
		return err
	}

	if flagJSON {
		if searches == nil {
			searches = []model.SavedSearch{}
		}
		printJSON(searches)
		return nil
	}

	if len(searches) == 0 {
		fmt.Println("No saved searches.")
		return nil
	}

	for _, ss := range searches {
		desc := ss.Query
		if ss.Filter.Type != "" {
			desc += " type:" + string(ss.Filter.Type)
		}
		if len(ss.Filter.Tags) > 0 {
			for _, t := range ss.Filter.Tags {
				desc += " tag:" + t
			}
		}
		if ss.Filter.Collection != "" {
			desc += " col:" + ss.Filter.Collection
		}
		fmt.Printf("  %-20s %s\n", ss.Name, desc)
	}
	return nil
}

func runSearchRun(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	ss, err := s.GetSavedSearch(ctx, args[0])
	if err != nil {
		return err
	}

	filter := ss.Filter
	filter.Query = ss.Query
	if filter.Limit <= 0 {
		filter.Limit = 50
	}

	var items []model.Item
	if filter.Query != "" {
		items, err = s.SearchItems(ctx, filter)
	} else {
		items, err = s.ListItems(ctx, filter)
	}
	if err != nil {
		return err
	}

	printItems(items)
	return nil
}

func runSearchDelete(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.DeleteSavedSearch(context.Background(), args[0]); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]string{"deleted": args[0]})
	} else {
		fmt.Printf("Deleted saved search %q\n", args[0])
	}
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
