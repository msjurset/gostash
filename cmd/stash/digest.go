package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var digestCmd = &cobra.Command{
	Use:   "digest",
	Short: "Render a Markdown summary of captures over a recent window",
	Long: `Generate a short Markdown report summarizing what's been captured
into Stash over a recent time window: counts by type, breakdown by
ingest source, top tags, items still flagged for action, and a few
resurface picks. Designed to drop into a weekly Runbook task that
writes the output to a file (or emails it).

Sources merged:
  - items table (captures within --since window)
  - capture.log (ingest surface attribution per item)
  - feed_candidates joined to items (per-feed-source attribution)
  - resurface scorer (for the "Re-visit" section)

Defaults to a 7-day window. Use --since to widen ("30d", "2w") or
narrow ("24h"). The output is plain Markdown — readable in any
editor, glamour-friendly, ready for email piping.`,
	RunE: runDigest,
}

func init() {
	digestCmd.Flags().String("since", "7d", "Time window: 24h, 7d, 2w, etc.")
	digestCmd.Flags().StringP("output", "o", "", "Write to FILE instead of stdout")
	digestCmd.Flags().IntP("limit-items", "l", 5, "Cap for each per-list section (top tags, re-visit, etc.)")
	rootCmd.AddCommand(digestCmd)
}

func runDigest(cmd *cobra.Command, _ []string) error {
	sinceStr, _ := cmd.Flags().GetString("since")
	output, _ := cmd.Flags().GetString("output")
	listLimit, _ := cmd.Flags().GetInt("limit-items")

	dur, err := parseLogDuration(sinceStr)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	since := time.Now().Add(-dur)

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	report, err := buildDigest(ctx, s, since, time.Now(), listLimit)
	if err != nil {
		return err
	}

	if output != "" {
		if err := os.WriteFile(output, []byte(report), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", output, err)
		}
		if !flagJSON {
			fmt.Printf("Wrote %d bytes to %s\n", len(report), output)
		}
		return nil
	}
	fmt.Print(report)
	return nil
}

// buildDigest assembles the Markdown body. Pulled out so it's testable
// and so a future caller can produce digests programmatically (e.g. a
// menubar widget).
func buildDigest(ctx context.Context, s store.Store, since, now time.Time, listLimit int) (string, error) {
	items, err := s.ListItems(ctx, model.ItemFilter{
		After: &since,
		Limit: 0,
	})
	if err != nil {
		return "", fmt.Errorf("list items: %w", err)
	}

	// Attribution: walk the capture.log to map item_id -> source.
	captureSources := map[string]string{}
	logPath := rules.DefaultLogPath(config.Dir())
	if events, err := rules.ReadEvents(logPath, 0); err == nil {
		for _, ev := range events {
			if ev.ItemID != "" && ev.Source != "" {
				// Newest entry wins (events are newest-first), so
				// only set if we haven't already.
				if _, seen := captureSources[ev.ItemID]; !seen {
					captureSources[ev.ItemID] = ev.Source
				}
			}
		}
	}

	// Feed-sourced attribution: feed_candidates with stashed_item_id
	// pointing at one of our window items.
	feedSources := map[string]string{}
	if cands, err := s.ListFeedCandidates(ctx, store.FeedCandidateFilter{
		States: []string{model.FeedStateStashed},
		Limit:  0,
	}); err == nil {
		for _, c := range cands {
			if c.StashedItemID != "" {
				feedSources[c.StashedItemID] = c.SourceName
			}
		}
	}

	var b strings.Builder
	headerDate := func(t time.Time) string {
		return t.Local().Format("Jan 2")
	}
	fmt.Fprintf(&b, "# Stash Digest — %s → %s\n\n", headerDate(since), headerDate(now))
	fmt.Fprintf(&b, "**%d %s captured.**\n\n", len(items), pluralize("item", len(items)))

	if len(items) == 0 {
		fmt.Fprintln(&b, "No captures in this window. Maybe a quiet week, maybe a memory-lane week.")
		return b.String(), nil
	}

	// By type
	typeCounts := countByField(items, func(i model.Item) string { return i.Type.Display() })
	if len(typeCounts) > 0 {
		fmt.Fprintln(&b, "## By type")
		for _, kv := range typeCounts {
			fmt.Fprintf(&b, "- %d %s\n", kv.count, kv.key)
		}
		fmt.Fprintln(&b)
	}

	// By source: prefer capture.log surface > feed source > fallback
	sourceCounts := countByField(items, func(i model.Item) string {
		if s, ok := captureSources[i.ID]; ok && s != "" {
			return prettySource(s)
		}
		if s, ok := feedSources[i.ID]; ok && s != "" {
			return s + " (feed)"
		}
		return "unattributed"
	})
	if len(sourceCounts) > 0 {
		fmt.Fprintln(&b, "## By source")
		for _, kv := range sourceCounts {
			fmt.Fprintf(&b, "- %d from %s\n", kv.count, kv.key)
		}
		fmt.Fprintln(&b)
	}

	// Top tags within the period
	tagCounts := map[string]int{}
	for _, item := range items {
		for _, t := range item.Tags {
			tagCounts[t.Name]++
		}
	}
	topTags := topN(tagCounts, listLimit)
	if len(topTags) > 0 {
		fmt.Fprintln(&b, "## Top tags")
		for _, kv := range topTags {
			fmt.Fprintf(&b, "- #%s (%d)\n", kv.key, kv.count)
		}
		fmt.Fprintln(&b)
	}

	// Worth acting on: items tagged read-later or watch-later, plus
	// completely untagged captures that probably need triage.
	queueItems := filterItems(items, func(i model.Item) bool {
		return hasAnyTag(i, "read-later", "watch-later")
	})
	if len(queueItems) > 0 {
		fmt.Fprintln(&b, "## Worth acting on")
		for _, it := range firstN(queueItems, listLimit) {
			fmt.Fprintf(&b, "- %s %s — %s\n",
				queueBadge(it),
				it.Title,
				it.CreatedAt.Local().Format("Jan 2"),
			)
		}
		fmt.Fprintln(&b)
	}
	untagged := filterItems(items, func(i model.Item) bool { return len(i.Tags) == 0 })
	if len(untagged) > 0 {
		fmt.Fprintln(&b, "## Untagged — needs triage")
		for _, it := range firstN(untagged, listLimit) {
			fmt.Fprintf(&b, "- %s — %s\n", it.Title, it.CreatedAt.Local().Format("Jan 2"))
		}
		fmt.Fprintln(&b)
	}

	// Re-visit: a few resurface picks (forgotten older items the
	// user might want to dust off). Best-effort — failure here doesn't
	// abort the digest.
	if picks, err := s.PickResurfaceItems(ctx, store.ResurfaceParams{Limit: listLimit}); err == nil && len(picks) > 0 {
		fmt.Fprintln(&b, "## Re-visit")
		for _, it := range picks {
			fmt.Fprintf(&b, "- %s — captured %s\n", it.Title, it.CreatedAt.Local().Format("Jan 2 2006"))
		}
		fmt.Fprintln(&b)
	}

	return b.String(), nil
}

// ───────────────────────────────────────────────────────────
// helpers
// ───────────────────────────────────────────────────────────

type kv struct {
	key   string
	count int
}

func countByField(items []model.Item, key func(model.Item) string) []kv {
	m := map[string]int{}
	for _, it := range items {
		m[key(it)]++
	}
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	return out
}

func topN(counts map[string]int, n int) []kv {
	out := make([]kv, 0, len(counts))
	for k, v := range counts {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func filterItems(items []model.Item, pred func(model.Item) bool) []model.Item {
	out := []model.Item{}
	for _, it := range items {
		if pred(it) {
			out = append(out, it)
		}
	}
	return out
}

func firstN(items []model.Item, n int) []model.Item {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func hasAnyTag(i model.Item, names ...string) bool {
	set := map[string]struct{}{}
	for _, n := range names {
		set[n] = struct{}{}
	}
	for _, t := range i.Tags {
		if _, ok := set[t.Name]; ok {
			return true
		}
	}
	return false
}

func queueBadge(i model.Item) string {
	if hasAnyTag(i, "watch-later") {
		return "[watch]"
	}
	if hasAnyTag(i, "read-later") {
		return "[read]"
	}
	return ""
}

func pluralize(noun string, n int) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// prettySource turns a raw capture.log source code into a user-facing
// label for the digest's "By source" breakdown. Shares the spirit of
// `describeSurface` in provenance.go but uses shorter labels suited
// to a list-bullet context.
func prettySource(s string) string {
	switch s {
	case "chrome":    return "Chrome extension"
	case "menubar":   return "menubar"
	case "selection": return "Selection Grabber"
	case "sortie":    return "Sortie folder watcher"
	case "service":   return "System Services"
	case "drag-drop": return "drag-and-drop"
	case "email":     return "email"
	case "cli":       return "CLI"
	case "fetch-url": return "Fetch URL picker"
	case "":          return "unattributed"
	default:          return s
	}
}
