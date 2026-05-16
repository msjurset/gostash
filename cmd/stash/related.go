package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var relatedCmd = &cobra.Command{
	Use:   "related <id>",
	Short: "Find items related to one by tags, links, domain, or content",
	Long: `Score every other item by overlap with the given item and return the
top matches. Signals (additive):

  +3 per shared tag
  +2 per shared collection
  +4 per existing manual link (either direction)
  +5 if content_hash matches exactly (true duplicate)
  +2 if the URL host (sans www.) matches

Archived items are excluded; the source item itself is excluded.
Used by the Mac app's "Related items" detail-pane section and
available standalone for ad-hoc graph exploration.`,
	Args: cobra.ExactArgs(1),
	RunE: runRelated,
}

func init() {
	relatedCmd.Flags().IntP("limit", "l", 5, "Maximum related items")
	rootCmd.AddCommand(relatedCmd)
}

func runRelated(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	source, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	items, err := s.RelatedItems(ctx, source, limit)
	if err != nil {
		return err
	}

	if flagJSON {
		return printJSONOrText(items)
	}
	if len(items) == 0 {
		fmt.Println("No related items.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tTITLE")
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\n", shortID(it.ID), it.Type.Display(), it.Title)
	}
	return w.Flush()
}
