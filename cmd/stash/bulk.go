package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var bulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Bulk operations on multiple items",
}

func init() {
	rootCmd.AddCommand(bulkCmd)
}

// addBulkFilterFlags registers the shared filter flags on a bulk subcommand.
func addBulkFilterFlags(cmd *cobra.Command) {
	cmd.Flags().String("query", "", "Full-text search query")
	cmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	cmd.Flags().StringSlice("tag", nil, "Filter by tag (repeatable)")
	cmd.Flags().String("in-collection", "", "Filter by collection membership")
	cmd.Flags().String("after", "", "Created after (YYYY-MM-DD)")
	cmd.Flags().String("before", "", "Created before (YYYY-MM-DD)")
	cmd.Flags().IntP("limit", "l", 0, "Max items to operate on")
}

// resolveItems collects items from positional args, filter flags, and stdin.
// Returns the deduplicated list of resolved items.
func resolveItems(cmd *cobra.Command, args []string, s store.Store) ([]model.Item, error) {
	ctx := context.Background()
	seen := map[string]bool{}
	var items []model.Item

	addItem := func(item *model.Item) {
		if !seen[item.ID] {
			seen[item.ID] = true
			items = append(items, *item)
		}
	}

	// 1. Resolve explicit IDs from positional args
	for _, id := range args {
		item, err := s.GetItem(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", id, err)
		}
		addItem(item)
	}

	// 2. Resolve from stdin if piped
	if !isTerminal() && len(args) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			id := strings.TrimSpace(scanner.Text())
			if id == "" {
				continue
			}
			item, err := s.GetItem(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("resolve stdin %q: %w", id, err)
			}
			addItem(item)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}

	// 3. Resolve from filter flags
	if hasFilterFlags(cmd) {
		filter, err := buildBulkFilter(cmd)
		if err != nil {
			return nil, err
		}
		var filtered []model.Item
		if filter.Query != "" {
			filtered, err = s.SearchItems(ctx, filter)
		} else {
			filtered, err = s.ListItems(ctx, filter)
		}
		if err != nil {
			return nil, fmt.Errorf("filter items: %w", err)
		}
		for i := range filtered {
			addItem(&filtered[i])
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no items selected — provide IDs, pipe from stdin, or use filter flags")
	}

	return items, nil
}

func hasFilterFlags(cmd *cobra.Command) bool {
	for _, name := range []string{"query", "type", "tag", "in-collection", "after", "before"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func buildBulkFilter(cmd *cobra.Command) (model.ItemFilter, error) {
	var f model.ItemFilter
	f.Query, _ = cmd.Flags().GetString("query")
	if v, _ := cmd.Flags().GetString("type"); v != "" {
		f.Type = model.ParseItemType(v)
	}
	if v, _ := cmd.Flags().GetStringSlice("tag"); len(v) > 0 {
		f.Tags = v
	}
	if v, _ := cmd.Flags().GetString("in-collection"); v != "" {
		f.Collection = v
	}
	if v, _ := cmd.Flags().GetString("after"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return f, fmt.Errorf("parse --after: %w", err)
		}
		f.After = &t
	}
	if v, _ := cmd.Flags().GetString("before"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return f, fmt.Errorf("parse --before: %w", err)
		}
		f.Before = &t
	}
	f.Limit, _ = cmd.Flags().GetInt("limit")
	return f, nil
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
