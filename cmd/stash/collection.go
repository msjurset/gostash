package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

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

var colMergeCmd = &cobra.Command{
	Use:   "merge <other>...",
	Short: "Merge other collections into a surviving collection",
	Long: `Fold the membership of every named collection into --into, then
delete the merged collections. Items already in the surviving
collection keep their positions; merged items append at the end in
their original relative order; duplicate memberships collapse
silently. Runs in a single transaction so a partial failure rolls
back.

Smart Collections (saved searches) can't be merged — there's no
stored membership to fold. Use 'stash collection add-to' to
snapshot a Smart Collection's current results into a Static one.

Examples:
  stash collection merge --into "Garden 2026" "May Flowers" "April Blooms"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCollectionMerge,
}

var colAddToCmd = &cobra.Command{
	Use:   "add-to",
	Short: "Add items from one or more collections into other collections",
	Long: `Upsert every item in the --from collection(s) into each --to
collection. Items already in a destination are no-ops; missing
items get added (existing positions preserved, new memberships
append). Sources can be Static OR Smart Collections — Smart
sources get their saved-search query executed and the live result
set is what gets added.

--create NAME creates a new Static Collection by that name and
adds it to the destination list — useful for promoting a Smart
Collection's current results into a frozen Static one in a single
move.

Examples:
  stash collection add-to --from "Garden 2026" --to "Inspiration"
  stash collection add-to --from "All Birds" --to "Backyard Birds"
  stash collection add-to --from "Recent Flowers" --create "May Flowers Snapshot"
  stash collection add-to --from src1 --from src2 --to dest1 --to dest2`,
	RunE: runCollectionAddTo,
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

func runCollectionMerge(cmd *cobra.Command, args []string) error {
	survivor, _ := cmd.Flags().GetString("into")
	others := args

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Sanity-check: every name resolves to a Static Collection.
	// Smart Collections (saved searches) share neither the table
	// nor the membership semantics — surface that clearly instead
	// of letting the store fail mid-transaction.
	all := append([]string{survivor}, others...)
	for _, name := range all {
		col, err := s.GetCollection(ctx, name)
		if err != nil || col == nil {
			// Could be a saved search — give a friendlier error.
			if ss, _ := s.GetSavedSearch(ctx, name); ss != nil {
				return fmt.Errorf("%q is a Smart Collection — merge is Static-only; use `stash collection add-to --from %q --to <static>` instead", name, name)
			}
			return fmt.Errorf("collection %q not found", name)
		}
	}

	if err := s.MergeCollections(ctx, survivor, others); err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{
			"survivor": survivor,
			"merged":   others,
		})
		return nil
	}
	fmt.Printf("✓ Merged %d collection(s) into %q.\n", len(others), survivor)
	return nil
}

func runCollectionAddTo(cmd *cobra.Command, _ []string) error {
	sources, _ := cmd.Flags().GetStringSlice("from")
	dests, _ := cmd.Flags().GetStringSlice("to")
	createName, _ := cmd.Flags().GetString("create")
	createDesc, _ := cmd.Flags().GetString("description")

	if len(sources) == 0 {
		return fmt.Errorf("at least one --from is required")
	}
	if len(dests) == 0 && createName == "" {
		return fmt.Errorf("at least one --to or --create is required")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Collect the union of every item across the source list.
	// Smart Collections (saved searches) resolve via their stored
	// filter + query; Static Collections resolve via their stored
	// membership rows. A name that matches BOTH (unusual but legal)
	// is treated as a Static.
	idSet := make(map[string]bool)
	for _, src := range sources {
		ids, err := resolveCollectionItemIDs(ctx, s, src)
		if err != nil {
			return fmt.Errorf("source %q: %w", src, err)
		}
		for _, id := range ids {
			idSet[id] = true
		}
	}
	if len(idSet) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "no items in source collection(s); nothing to do")
		return nil
	}

	// Materialize the destination list, creating the new
	// collection first so it's a fresh target for the upsert.
	allDests := append([]string{}, dests...)
	if createName != "" {
		if _, err := s.CreateCollection(ctx, createName, createDesc); err != nil {
			return fmt.Errorf("create %q: %w", createName, err)
		}
		allDests = append(allDests, createName)
	}
	// Sanity-check destinations: must be Static. Surface a clear
	// error if the user passed a Smart Collection name.
	for _, dest := range allDests {
		col, err := s.GetCollection(ctx, dest)
		if err != nil || col == nil {
			if ss, _ := s.GetSavedSearch(ctx, dest); ss != nil {
				return fmt.Errorf("%q is a Smart Collection — destinations must be Static", dest)
			}
			return fmt.Errorf("destination %q not found", dest)
		}
	}

	// Upsert: AddToCollection's INSERT OR IGNORE skips items
	// already in the destination, so re-running this command is
	// idempotent.
	added := 0
	skipped := 0
	var errs []string
	for id := range idSet {
		for _, dest := range allDests {
			if err := s.AddToCollection(ctx, id, dest); err != nil {
				errs = append(errs, fmt.Sprintf("%s → %s: %v", shortID(id), dest, err))
				continue
			}
			added++
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"sources":     sources,
			"destinations": allDests,
			"created":     createName,
			"items":       len(idSet),
			"memberships_added": added,
			"errors":      len(errs),
		})
	} else {
		_ = skipped // counted in the loop only if we surface per-skip in future
		fmt.Printf("✓ Added %d membership(s) from %d item(s) → %d destination(s).\n",
			added, len(idSet), len(allDests))
		if createName != "" {
			fmt.Printf("  (created %q)\n", createName)
		}
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ %s\n", e)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s)", len(errs))
	}
	return nil
}

// resolveCollectionItemIDs returns the item-ID set for a collection
// reference, dispatching on whether the name belongs to a Static
// Collection or a Smart Collection (saved search). Names that match
// neither return an error.
func resolveCollectionItemIDs(ctx context.Context, s store.Store, name string) ([]string, error) {
	// Try Static first — both shapes share names in user-facing
	// usage, but a Static lookup is cheaper and the more common
	// case.
	if col, err := s.GetCollection(ctx, name); err == nil && col != nil {
		items, err := s.ListCollectionItems(ctx, name, model.ItemFilter{Limit: 0})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		return ids, nil
	}
	// Fall back to Smart — run its saved filter against the
	// current store. Empty Query → ListItems; non-empty → search.
	ss, err := s.GetSavedSearch(ctx, name)
	if err != nil || ss == nil {
		return nil, fmt.Errorf("not found")
	}
	filter := ss.Filter
	filter.Query = ss.Query
	if filter.Limit <= 0 {
		filter.Limit = 0 // unlimited — we want the whole set
	}
	var items []model.Item
	if filter.Query != "" {
		items, err = s.SearchItems(ctx, filter)
	} else {
		items, err = s.ListItems(ctx, filter)
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids, nil
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
	colMergeCmd.Flags().String("into", "", "Surviving collection name (required)")
	_ = colMergeCmd.MarkFlagRequired("into")
	colAddToCmd.Flags().StringSlice("from", nil, "Source collection(s) — Static or Smart")
	colAddToCmd.Flags().StringSlice("to", nil, "Destination Static Collection(s)")
	colAddToCmd.Flags().String("create", "", "Create this Static Collection and add it as a destination")
	colAddToCmd.Flags().String("description", "", "Description for the new collection (only with --create)")
	collectionCmd.AddCommand(colListCmd)
	collectionCmd.AddCommand(colCreateCmd)
	collectionCmd.AddCommand(colDeleteCmd)
	collectionCmd.AddCommand(colShowCmd)
	collectionCmd.AddCommand(colReorderCmd)
	collectionCmd.AddCommand(colTouchCmd)
	collectionCmd.AddCommand(colMergeCmd)
	collectionCmd.AddCommand(colAddToCmd)
	rootCmd.AddCommand(collectionCmd)
}
