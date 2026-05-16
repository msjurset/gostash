package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var resurfaceCmd = &cobra.Command{
	Use:   "resurface",
	Short: "Surface forgotten stash items for the Inbox's 'From your stash' section",
	Long: `Pick a handful of items the user hasn't looked at recently and present
them as "stuff you saved that might be worth a revisit." Companion to
'stash feeds' — together they make up the Inbox.

The scorer favors curation effort (items with notes/tags/links rank
higher) and mildly damps very old captures. Items the user has
dismissed are cooled-down for ~6 months; snoozed items skip until
their snooze_until passes.

Subcommands let the user dismiss / snooze individual picks; the bare
'stash resurface' command prints today's selection.`,
	RunE: runResurfacePick,
}

var resurfaceDismissCmd = &cobra.Command{
	Use:   "dismiss <id>",
	Short: "Stop resurfacing this item (cooldown ~6mo)",
	Args:  cobra.ExactArgs(1),
	RunE:  runResurfaceDismiss,
}

var resurfaceSnoozeCmd = &cobra.Command{
	Use:   "snooze <id>",
	Short: "Skip this item until --for elapses",
	Args:  cobra.ExactArgs(1),
	RunE:  runResurfaceSnooze,
}

func init() {
	resurfaceCmd.Flags().IntP("limit", "l", 5, "Maximum picks")
	resurfaceCmd.Flags().Duration("min-idle", 30*24*time.Hour, "Only pick items not seen for at least this long")
	resurfaceCmd.Flags().Bool("mark", false, "Mark the picked items as resurfaced so they don't repeat soon")

	resurfaceSnoozeCmd.Flags().Duration("for", 7*24*time.Hour, "How long to snooze")

	resurfaceCmd.AddCommand(resurfaceDismissCmd, resurfaceSnoozeCmd)
	rootCmd.AddCommand(resurfaceCmd)
}

func runResurfacePick(cmd *cobra.Command, _ []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	minIdle, _ := cmd.Flags().GetDuration("min-idle")
	mark, _ := cmd.Flags().GetBool("mark")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	items, err := s.PickResurfaceItems(ctx, store.ResurfaceParams{
		Limit:      limit,
		MinIdleAgo: minIdle,
	})
	if err != nil {
		return err
	}
	if mark {
		now := time.Now().UTC()
		for _, it := range items {
			_ = s.MarkResurfaced(ctx, it.ID, now)
		}
	}
	if flagJSON {
		printJSONSlice(items)
		return nil
	}
	if len(items) == 0 {
		fmt.Println("No items to resurface.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tAGE\tTITLE")
	now := time.Now()
	for _, it := range items {
		age := now.Sub(it.CreatedAt)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", shortID(it.ID), it.Type.Display(), humanDuration(age), it.Title)
	}
	return w.Flush()
}

func runResurfaceDismiss(_ *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	id, err := resolveItemID(s, args[0])
	if err != nil {
		return err
	}
	if err := s.DismissResurface(context.Background(), id, time.Now().UTC()); err != nil {
		return err
	}
	if !flagJSON {
		fmt.Printf("Dismissed %s from resurface for ~6 months\n", shortID(id))
	}
	return nil
}

func runResurfaceSnooze(cmd *cobra.Command, args []string) error {
	dur, _ := cmd.Flags().GetDuration("for")
	until := time.Now().UTC().Add(dur)
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	id, err := resolveItemID(s, args[0])
	if err != nil {
		return err
	}
	if err := s.SnoozeResurface(context.Background(), id, until); err != nil {
		return err
	}
	if !flagJSON {
		fmt.Printf("Snoozed %s until %s\n", shortID(id), until.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

// resolveItemID handles short-id prefix lookups so the user can pass
// `stash resurface dismiss 01KR9HKVDG` instead of the full ULID.
func resolveItemID(s store.Store, arg string) (string, error) {
	item, err := s.GetItem(context.Background(), arg)
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

func humanDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days >= 730:
		return fmt.Sprintf("%dy", days/365)
	case days >= 60:
		return fmt.Sprintf("%dmo", days/30)
	case days >= 14:
		return fmt.Sprintf("%dw", days/7)
	case days >= 1:
		return fmt.Sprintf("%dd", days)
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

