package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/msjurset/gostash/internal/config"
	"github.com/spf13/cobra"
)

var tagLogCmd = &cobra.Command{
	Use:   "tag-log",
	Short: "Show recent manual tag activity",
	Long: `Show events from $STASH_DIR/tags.log. Each entry is one tag mutation
(add or remove) made by the user via 'stash edit', 'stash bulk tag',
or the Mac app. Rule-driven and capture-time tag application are not
recorded here — those live in capture.log.

Newest first. Use --json for machine-readable output (the Mac
suggest-rules path consumes this directly).`,
	RunE: runTagLog,
}

func init() {
	tagLogCmd.Flags().StringP("action", "a", "", "Filter by action (add, remove)")
	tagLogCmd.Flags().StringP("tag", "t", "", "Filter to events involving the named tag")
	tagLogCmd.Flags().IntP("limit", "l", 50, "Maximum events to show (0 = all)")
	tagLogCmd.Flags().String("since", "", "Only show events newer than DURATION (e.g. 30m, 1h, 24h, 7d, 1w)")
	rootCmd.AddCommand(tagLogCmd)
}

func runTagLog(cmd *cobra.Command, args []string) error {
	actionFilter, _ := cmd.Flags().GetString("action")
	tagFilter, _ := cmd.Flags().GetString("tag")
	limit, _ := cmd.Flags().GetInt("limit")
	sinceStr, _ := cmd.Flags().GetString("since")

	if actionFilter != "" && actionFilter != "add" && actionFilter != "remove" {
		return fmt.Errorf("invalid --action %q (want add or remove)", actionFilter)
	}

	var since time.Time
	if sinceStr != "" {
		d, err := parseLogDuration(sinceStr)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		since = time.Now().Add(-d)
	}

	path := audit.DefaultTagsLogPath(config.Dir())
	events, err := audit.ReadTagEvents(path, 0)
	if err != nil {
		return err
	}
	events = filterTagEvents(events, actionFilter, tagFilter, since)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(events)
	}

	if len(events) == 0 {
		fmt.Println("No tag events.")
		return nil
	}
	for _, ev := range events {
		emitTagEvent(ev)
	}
	return nil
}

func filterTagEvents(events []audit.TagEvent, action, tag string, since time.Time) []audit.TagEvent {
	if action == "" && tag == "" && since.IsZero() {
		return events
	}
	out := events[:0]
	for _, ev := range events {
		if action != "" && string(ev.Action) != action {
			continue
		}
		if tag != "" && !strings.EqualFold(ev.Tag, tag) {
			continue
		}
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func emitTagEvent(ev audit.TagEvent) {
	sym := "+"
	if ev.Action == audit.ActionRemove {
		sym = "−"
	}
	stamp := ev.Timestamp.Local().Format("2006-01-02 15:04")
	domain := ev.ItemDomain
	if domain == "" {
		domain = "—"
	}
	source := ev.Source
	if source == "" {
		source = "?"
	}
	fmt.Printf("%s %s %s #%s  [%s] %s  (%s)\n",
		stamp, sym, source, ev.Tag, shortID(ev.ItemID), domain, ev.ItemType)
}
