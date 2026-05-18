package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var collectionCmd = &cobra.Command{
	Use:     "collection",
	Aliases: []string{"col"},
	Short:   "Manage collections",
}

var colListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all collections",
	Long: `List collections. Default sort is alphabetical by name; --sort
flips to "recent" (newest MAX(added_at) per collection — what the
Mac sidebar's Recent toggle uses) or "frequent" (view_count DESC,
backing the Frequent toggle). --limit caps the output; useful
together with --sort recent to grab just the top N for a sidebar
render.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		sortMode, _ := cmd.Flags().GetString("sort")
		limit, _ := cmd.Flags().GetInt("limit")
		ctx := context.Background()

		var cols []model.Collection
		switch sortMode {
		case "", "name":
			cols, err = s.ListCollections(ctx)
		case "recent":
			cols, err = s.ListCollectionsByRecentActivity(ctx, limit)
		case "frequent":
			cols, err = s.ListCollectionsByFrequency(ctx, limit)
		default:
			return fmt.Errorf("unknown --sort %q (want name, recent, or frequent)", sortMode)
		}
		if err != nil {
			return err
		}
		// Honor --limit for the name-sorted path too (the typed
		// store methods already cap, but ListCollections doesn't).
		if sortMode == "" || sortMode == "name" {
			if limit > 0 && len(cols) > limit {
				cols = cols[:limit]
			}
		}
		printCollections(cols)
		return nil
	},
}

var colTouchCmd = &cobra.Command{
	Use:   "touch <name>",
	Short: "Increment a collection's view_count (Frequent-sort signal)",
	Long: `Bumps the collection's view_count by 1. Called by the Mac sidebar
when the user navigates to a collection so the "Frequent" sort
mode reflects actual usage. Silent no-op on a missing name —
keeps a stale post-rename click from erroring.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()
		return s.TouchCollection(context.Background(), args[0])
	},
}

var colCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		desc, _ := cmd.Flags().GetString("description")
		col, err := s.CreateCollection(context.Background(), args[0], desc)
		if err != nil {
			return err
		}

		if flagJSON {
			printJSON(col)
		} else {
			fmt.Printf("Created collection %q\n", col.Name)
		}
		return nil
	},
}

var colDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a collection (items are kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if err := s.DeleteCollection(context.Background(), args[0]); err != nil {
			return err
		}

		if flagJSON {
			printJSON(map[string]string{"deleted": args[0]})
		} else {
			fmt.Printf("Deleted collection %q\n", args[0])
		}
		return nil
	},
}

var colShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show items in a collection",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		ctx := context.Background()
		col, err := s.GetCollection(ctx, args[0])
		if err != nil {
			return err
		}

		items, err := s.ListCollectionItems(ctx, col.Name, model.ItemFilter{})
		if err != nil {
			return err
		}

		if !flagJSON {
			fmt.Printf("Collection: %s\n", col.Name)
			if col.Description != "" {
				fmt.Printf("Description: %s\n", col.Description)
			}
			fmt.Println()
		}
		printItems(items)
		return nil
	},
}

var colReorderCmd = &cobra.Command{
	Use:   "reorder <name> <id>...",
	Short: "Set the curated order of items within a collection",
	Long: `Reorder items inside a collection. Each id is positioned in the
order it appears in the argument list (first id → position 0).
Items not listed retain their existing positions.

Use stdin (one id per line) when the list is too long for argv:

  stash collection reorder mark-favs - <<EOF
  01ABC...
  01DEF...
  01GHI...
  EOF

The Mac app calls this on drag-to-reorder inside the masonry /
list view; rules can also call it programmatically.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runReorderCollection,
}

func runReorderCollection(cmd *cobra.Command, args []string) error {
	name := args[0]
	var ids []string
	if len(args) > 1 {
		// Stdin sentinel: `-` means read ids from stdin.
		if len(args) == 2 && args[1] == "-" {
			ids = readIDLines(cmd)
		} else {
			ids = args[1:]
		}
	} else {
		ids = readIDLines(cmd)
	}
	if len(ids) == 0 {
		return fmt.Errorf("no ids supplied")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.ReorderCollection(context.Background(), name, ids); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]any{
			"collection": name,
			"reordered":  len(ids),
		})
	} else {
		fmt.Printf("Reordered %d items in collection %q\n", len(ids), name)
	}
	return nil
}

// readIDLines reads ids from stdin, one per line. Empty lines and
// surrounding whitespace are stripped. Used by `stash collection
// reorder` when the list exceeds argv-friendly size.
func readIDLines(cmd *cobra.Command) []string {
	var out []string
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func init() {
	colCreateCmd.Flags().StringP("description", "d", "", "Collection description")
	colListCmd.Flags().String("sort", "name", "Sort mode: name | recent | frequent")
	colListCmd.Flags().Int("limit", 0, "Maximum number of collections to return (0 = all)")
	collectionCmd.AddCommand(colListCmd)
	collectionCmd.AddCommand(colCreateCmd)
	collectionCmd.AddCommand(colDeleteCmd)
	collectionCmd.AddCommand(colShowCmd)
	collectionCmd.AddCommand(colReorderCmd)
	collectionCmd.AddCommand(colTouchCmd)
	rootCmd.AddCommand(collectionCmd)
}
