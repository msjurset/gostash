package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/spf13/cobra"
)

var archiveCmd = &cobra.Command{
	Use:   "archive <id>...",
	Short: "Soft-delete one or more items (hide from default list/search)",
	Long: `Mark items as archived. Archived items stay in the database (file
blob, tags, links, collections all preserved) but don't appear in
'stash list' or 'stash search' by default. Use --include-archived
or --archived on those commands to see them, or 'stash unarchive'
to restore.

Designed to be driven by scheduled curation runbooks. Example:

  stash list --tag read-later --before 2025-11-06 --json |
      jq -r '.[].id' |
      xargs -n1 stash archive --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetArchived(cmd, args, true)
	},
}

var unarchiveCmd = &cobra.Command{
	Use:   "unarchive <id>...",
	Short: "Restore an archived item to the default view",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSetArchived(cmd, args, false)
	},
}

func runSetArchived(cmd *cobra.Command, ids []string, archived bool) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	verb := "archived"
	if !archived {
		verb = "unarchived"
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	type previewRow struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var changed []string
	var preview []previewRow
	var errs []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if dryRun {
			// Resolve the ID to a real item so the user sees titles
			// rather than opaque ULIDs in the preview. Missing IDs
			// surface as errors so a typo doesn't fall through silent.
			item, err := s.GetItem(ctx, id)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", id, err))
				continue
			}
			preview = append(preview, previewRow{ID: item.ID, Title: item.Title})
			continue
		}
		if err := s.SetArchived(ctx, id, archived); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		
		// Log the audit event so it shows in the UI activity list
		if item, _ := s.GetItem(ctx, id); item != nil {
			action := audit.ActionRemove
			if archived {
				action = audit.ActionAdd
			}
			logTagAudit(item, action, "archived", "edit")
		}
		
		changed = append(changed, id)
	}

	if flagJSON {
		out := map[string]any{
			"action": verb,
			"errors": errs,
		}
		if dryRun {
			out["dry_run"] = true
			out["would_change"] = preview
		} else {
			out["changed"] = changed
		}
		printJSON(out)
	} else if dryRun {
		// "verb" is past tense ("archived"); strip the trailing "d"
		// for the infinitive form used in the dry-run preamble.
		infinitive := strings.TrimSuffix(verb, "d")
		fmt.Printf("Dry run — would %s %d item(s):\n", infinitive, len(preview))
		for _, p := range preview {
			title := p.Title
			if title == "" {
				title = "(no title)"
			}
			fmt.Printf("  %s  %s\n", shortID(p.ID), title)
		}
		for _, e := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), "skip:", e)
		}
	} else {
		for _, id := range changed {
			fmt.Printf("%s %s\n", strings.ToUpper(verb[:1])+verb[1:], shortID(id))
		}
		for _, e := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), "skip:", e)
		}
	}

	if !dryRun && len(changed) == 0 && len(errs) > 0 {
		return fmt.Errorf("no items %s", verb)
	}
	return nil
}

func init() {
	archiveCmd.Flags().Bool("dry-run", false, "Print what would be archived without making changes")
	unarchiveCmd.Flags().Bool("dry-run", false, "Print what would be unarchived without making changes")
	rootCmd.AddCommand(archiveCmd)
	rootCmd.AddCommand(unarchiveCmd)
}
