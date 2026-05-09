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
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		cols, err := s.ListCollections(context.Background())
		if err != nil {
			return err
		}
		printCollections(cols)
		return nil
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
	collectionCmd.AddCommand(colListCmd)
	collectionCmd.AddCommand(colCreateCmd)
	collectionCmd.AddCommand(colDeleteCmd)
	collectionCmd.AddCommand(colShowCmd)
	collectionCmd.AddCommand(colReorderCmd)
	rootCmd.AddCommand(collectionCmd)
}
