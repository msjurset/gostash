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
  stash search --regex "^https"   # regex-only search (no positional needed)
  stash search save fav-go --type url --tag go    # save a search
  stash search list               # list saved searches
  stash search run fav-go         # run a saved search
  stash search delete fav-go      # delete a saved search`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) >= 1 {
			return nil
		}
		// Regex-only invocation skips the positional query.
		if r, _ := cmd.Flags().GetString("regex"); r != "" {
			return nil
		}
		return fmt.Errorf("requires a query argument or --regex pattern")
	},
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

var searchRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a saved search",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()
		if err := s.RenameSavedSearch(context.Background(), args[0], args[1]); err != nil {
			return err
		}
		if flagJSON {
			printJSON(map[string]string{"old": args[0], "new": args[1]})
		} else {
			fmt.Printf("Renamed saved search %q → %q\n", args[0], args[1])
		}
		return nil
	},
}

func init() {
	addSearchFilterFlags(searchCmd)
	addSearchFilterFlags(searchSaveCmd)
	searchSaveCmd.Flags().Bool("live", false, "Save as a Smart Collection (auto-refreshes in stash-mac sidebar)")
	searchCmd.AddCommand(searchSaveCmd)
	searchCmd.AddCommand(searchListCmd)
	searchCmd.AddCommand(searchRunCmd)
	searchCmd.AddCommand(searchDeleteCmd)
	searchCmd.AddCommand(searchRenameCmd)
	rootCmd.AddCommand(searchCmd)
}

func addSearchFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	cmd.Flags().StringSlice("tag", nil, "Filter by tag (repeatable)")
	cmd.Flags().StringSlice("exclude-tag", nil, "Exclude items carrying any of these tags (repeatable)")
	cmd.Flags().Bool("untagged", false, "Only items with no tags")
	cmd.Flags().String("collection", "", "Filter by collection")
	cmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	cmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	cmd.Flags().String("recent", "", "Only items captured within this duration (e.g. 7d, 2w, 6h). Resolved at query time.")
	cmd.Flags().String("regex", "", "RE2 pattern matched against title+notes+url+extracted text. Prefix with `!` to negate (e.g. `!^http://`).")
	cmd.Flags().IntP("limit", "l", 50, "Max results")
	cmd.Flags().Bool("include-archived", false, "Also show archived items")
	cmd.Flags().Bool("archived", false, "Show only archived items (overrides --include-archived)")
}

func runSearch(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	query := ""
	if len(args) > 0 {
		query = args[0]
	}
	filter, err := buildFilter(cmd, query)
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

	live, _ := cmd.Flags().GetBool("live")
	if err := s.SaveSearch(context.Background(), name, query, filter, live); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]any{"saved": name, "live": live})
	} else {
		kind := "search"
		if live {
			kind = "smart collection"
		}
		fmt.Printf("Saved %s %q\n", kind, name)
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
		marker := " "
		if ss.Live {
			marker = "*"
		}
		fmt.Printf("%s %-20s %s\n", marker, ss.Name, desc)
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
	if v, _ := cmd.Flags().GetStringSlice("exclude-tag"); len(v) > 0 {
		f.ExcludeTags = v
	}
	if v, _ := cmd.Flags().GetBool("untagged"); v {
		f.Untagged = true
	}
	if v, _ := cmd.Flags().GetString("recent"); v != "" {
		f.Recent = v
	}
	if v, _ := cmd.Flags().GetString("regex"); v != "" {
		f.Regex = v
	}
	f.Limit, _ = cmd.Flags().GetInt("limit")
	// `--archived` (only) wins over `--include-archived` (both). Both
	// flags are advisory — when neither is set, the default filter
	// excludes archived items so they stay out of casual browsing.
	if v, _ := cmd.Flags().GetBool("archived"); v {
		f.OnlyArchived = true
	} else if v, _ := cmd.Flags().GetBool("include-archived"); v {
		f.IncludeArchived = true
	}
	return f, nil
}
