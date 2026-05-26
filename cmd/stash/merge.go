package main

import (
	"context"
	"fmt"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/rules"

	"github.com/spf13/cobra"
)

var mergeCmd = &cobra.Command{
	Use:   "merge <target-id> <source-id> [<source-id>...]",
	Short: "Merge source items into target as attached files",
	Long: `Folds every source item into target:

  - Each source's primary file becomes an attached file on target,
    in argument order.
  - Each source's attached files re-parent to target (appended after
    the source primaries).
  - Source tags union into target.
  - Source notes append below target's notes separated by "---".
  - Source items are then deleted.

Use when several separate captures actually describe the same
subject and you want them collapsed into one logical item — e.g.
three separate uploads of the same mushroom from different angles.

Examples:
  stash merge 01KR9HKVDG 01KSPECIESA 01KSPECIESB
  stash merge --dry-run 01KR9HKVDG 01KSPECIESA   # report only`,
	Args: cobra.MinimumNArgs(2),
	RunE: runMerge,
}

func init() {
	mergeCmd.Flags().Bool("dry-run", false, "Print what would happen without writing")
	rootCmd.AddCommand(mergeCmd)
}

func runMerge(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	target := args[0]
	sources := args[1:]

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Resolve every id up front so a typo doesn't half-merge the
	// rest of the batch.
	tgt, err := s.GetItem(ctx, target)
	if err != nil {
		return fmt.Errorf("target %s: %w", target, err)
	}
	resolvedSources := make([]string, 0, len(sources))
	for _, sid := range sources {
		src, err := s.GetItem(ctx, sid)
		if err != nil {
			return fmt.Errorf("source %s: %w", sid, err)
		}
		resolvedSources = append(resolvedSources, src.ID)
	}

	if dryRun {
		if flagJSON {
			printJSON(map[string]any{
				"target":  tgt.ID,
				"sources": resolvedSources,
				"would_merge": map[string]any{
					"files":  "each source primary + attached files → target attachments",
					"tags":   "union",
					"notes":  "append below target notes separated by ---",
					"delete": "sources deleted on success",
				},
			})
		} else {
			fmt.Printf("Would merge into target [%s] %s:\n", shortID(tgt.ID), tgt.Title)
			for _, sid := range resolvedSources {
				src, _ := s.GetItem(ctx, sid)
				fmt.Printf("  + [%s] %s\n", shortID(src.ID), src.Title)
			}
		}
		return nil
	}

	out, err := s.MergeItems(ctx, tgt.ID, resolvedSources)
	if err != nil {
		return err
	}
	// Capture-log entry on the surviving target so the activity /
	// provenance view shows that this row absorbed others. Source
	// IDs persist in the event so even though the source rows are
	// gone from the DB, the audit trail still tells you what got
	// folded in and when. Best-effort — a log write failure
	// doesn't undo the merge.
	_ = rules.AppendEvent(rules.DefaultLogPath(config.Dir()), rules.Event{
		Timestamp: time.Now().UTC(),
		Type:      rules.EventMerge,
		ItemID:    out.ID,
		Title:     out.Title,
		Source:    "stash merge",
		Sources:   resolvedSources,
	})
	if flagJSON {
		printJSON(out)
	} else {
		fmt.Printf("Merged %d source(s) into [%s] %s.\n",
			len(resolvedSources), shortID(out.ID), out.Title)
		fmt.Printf("Target now has %d attached file(s).\n", len(out.Files))
	}
	return nil
}
