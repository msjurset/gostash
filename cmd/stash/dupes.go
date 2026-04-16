package main

import (
	"context"
	"fmt"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/similarity"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var dupesCmd = &cobra.Command{
	Use:   "dupes",
	Short: "Find duplicate items",
	Long: `Find items that may be duplicates based on content hash, URL, or similar titles.

  stash dupes                        # all duplicate checks
  stash dupes --type url             # only check URL items
  stash dupes --threshold 0.8        # stricter title similarity
  stash dupes --include-dismissed    # include previously dismissed pairs`,
	RunE: runDupes,
}

var dupesDismissCmd = &cobra.Command{
	Use:   "dismiss <id1> <id2>",
	Short: "Dismiss a duplicate pair",
	Args:  cobra.ExactArgs(2),
	RunE:  runDupesDismiss,
}

func init() {
	dupesCmd.Flags().String("type", "", "Filter by type (url, snippet, file, image)")
	dupesCmd.Flags().Float64("threshold", 0.7, "Title similarity threshold (0.0-1.0)")
	dupesCmd.Flags().Bool("include-dismissed", false, "Include previously dismissed pairs")
	dupesCmd.AddCommand(dupesDismissCmd)
	rootCmd.AddCommand(dupesCmd)
}

func runDupes(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	filter := model.ItemFilter{Limit: 100000}
	if v, _ := cmd.Flags().GetString("type"); v != "" {
		filter.Type = model.ParseItemType(v)
	}
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	includeDismissed, _ := cmd.Flags().GetBool("include-dismissed")

	items, err := s.ListItems(ctx, filter)
	if err != nil {
		return err
	}

	// Load dismissed pairs for filtering
	var dismissed map[[2]string]bool
	if !includeDismissed {
		dismissed = loadDismissedSet(ctx, s)
	}

	var results []model.DupeResult

	// 1. Content hash duplicates
	hashGroups := groupBy(items, func(item model.Item) string {
		if item.ContentHash == "" {
			return ""
		}
		return item.ContentHash
	})
	for key, group := range hashGroups {
		if len(group) > 1 {
			issues := toCheckIssues(group)
			if !includeDismissed {
				issues = filterDismissedIssues(issues, dismissed)
			}
			if len(issues) > 1 {
				results = append(results, model.DupeResult{
					Method: "hash",
					Key:    key[:16] + "...",
					Items:  issues,
				})
			}
		}
	}

	// 2. URL duplicates
	urlGroups := groupBy(items, func(item model.Item) string {
		if item.URL == "" {
			return ""
		}
		return item.URL
	})
	for key, group := range urlGroups {
		if len(group) > 1 {
			issues := toCheckIssues(group)
			if !includeDismissed {
				issues = filterDismissedIssues(issues, dismissed)
			}
			if len(issues) > 1 {
				results = append(results, model.DupeResult{
					Method: "url",
					Key:    key,
					Items:  issues,
				})
			}
		}
	}

	// 3. Similar titles
	type pair struct{ i, j int }
	seen := map[pair]bool{}
	titleGroups := map[string][]model.CheckIssue{}

	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].Title == "" || items[j].Title == "" {
				continue
			}
			if seen[pair{i, j}] {
				continue
			}
			if !includeDismissed && isDismissed(items[i].ID, items[j].ID, dismissed) {
				continue
			}
			score := similarity.Score(items[i].Title, items[j].Title)
			if score >= threshold && items[i].Title != items[j].Title {
				key := items[i].Title
				if _, ok := titleGroups[key]; !ok {
					titleGroups[key] = []model.CheckIssue{{
						ID:    items[i].ID,
						Title: items[i].Title,
					}}
				}
				titleGroups[key] = append(titleGroups[key], model.CheckIssue{
					ID:     items[j].ID,
					Title:  items[j].Title,
					Detail: fmt.Sprintf("%.0f%% similar", score*100),
				})
				seen[pair{i, j}] = true
			}
		}
	}
	for key, group := range titleGroups {
		if len(group) > 1 {
			results = append(results, model.DupeResult{
				Method: "title",
				Key:    key,
				Items:  group,
			})
		}
	}

	if flagJSON {
		if results == nil {
			results = []model.DupeResult{}
		}
		printJSON(results)
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No duplicates found.")
		return nil
	}

	for _, r := range results {
		fmt.Printf("[%s] %s\n", r.Method, truncate(r.Key, 60))
		for _, item := range r.Items {
			detail := ""
			if item.Detail != "" {
				detail = " — " + item.Detail
			}
			fmt.Printf("  [%s] %s%s\n", shortID(item.ID), item.Title, detail)
		}
		fmt.Println()
	}

	fmt.Printf("%d duplicate group(s) found.\n", len(results))
	return nil
}

func runDupesDismiss(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.DismissDupePair(context.Background(), args[0], args[1]); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]string{"dismissed": args[0] + "+" + args[1]})
	} else {
		fmt.Printf("Dismissed duplicate pair [%s] [%s]\n", shortID(args[0]), shortID(args[1]))
	}
	return nil
}

func loadDismissedSet(ctx context.Context, s store.Store) map[[2]string]bool {
	pairs, err := s.ListDismissedPairs(ctx)
	if err != nil {
		return nil
	}
	set := make(map[[2]string]bool, len(pairs))
	for _, p := range pairs {
		set[p] = true
	}
	return set
}

func isDismissed(idA, idB string, dismissed map[[2]string]bool) bool {
	if dismissed == nil {
		return false
	}
	a, b := idA, idB
	if a > b {
		a, b = b, a
	}
	return dismissed[[2]string{a, b}]
}

// filterDismissedIssues removes items from a group where all pairs involving
// that item have been dismissed. Returns the remaining issues.
func filterDismissedIssues(issues []model.CheckIssue, dismissed map[[2]string]bool) []model.CheckIssue {
	if dismissed == nil || len(issues) < 2 {
		return issues
	}
	// Check if ALL pairs in this group are dismissed
	allDismissed := true
	for i := 0; i < len(issues) && allDismissed; i++ {
		for j := i + 1; j < len(issues); j++ {
			if !isDismissed(issues[i].ID, issues[j].ID, dismissed) {
				allDismissed = false
				break
			}
		}
	}
	if allDismissed {
		return nil
	}
	return issues
}

func groupBy(items []model.Item, key func(model.Item) string) map[string][]model.Item {
	groups := map[string][]model.Item{}
	for _, item := range items {
		k := key(item)
		if k == "" {
			continue
		}
		groups[k] = append(groups[k], item)
	}
	return groups
}

func toCheckIssues(items []model.Item) []model.CheckIssue {
	issues := make([]model.CheckIssue, len(items))
	for i, item := range items {
		issues[i] = model.CheckIssue{ID: item.ID, Title: item.Title}
	}
	return issues
}
