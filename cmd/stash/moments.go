package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

// momentsCmd surfaces clusters of items captured close together
// in time (and ideally sharing a location or tag) as candidate
// trip / event / "moment" collections. Pure suggestion engine —
// doesn't mutate the stash until the user runs `stash moments
// accept`. Time precedence: CapturedAt → CreatedAt fallback (see
// clusterTime helper) so a burst of photos all stashed days later
// still clusters by when they were actually taken.
var momentsCmd = &cobra.Command{
	Use:   "moments",
	Short: "Suggest trip / event collections from time-clustered items",
	Long: `Walks recent items, groups them into time-clusters separated by a
configurable gap, and surfaces clusters that look like trips or events:
multiple items captured close together, often sharing a location or a
tag. Each suggestion is a candidate collection the user can accept
into the stash. Time precedence: items.captured_at (EXIF / email
headers / file mtime) → items.created_at fallback.

Cluster scoring favors size, location coherence, and shared-tag
density. Suggestions with all items already belonging to the same
existing collection are dropped — those have already been actioned.

  stash moments                       — list current suggestions
  stash moments --json                — JSON list (Mac UI / scripts)
  stash moments accept --name NAME    — create collection + add items
       ID ID ID

Defaults to scanning the last 90d; --all widens to the whole stash.`,
	RunE: runMoments,
}

var momentsAcceptCmd = &cobra.Command{
	Use:   "accept ID...",
	Short: "Accept a suggestion: create a collection and add the items",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runMomentsAccept,
}

func init() {
	momentsCmd.Flags().Duration("max-gap", 6*time.Hour, "Gap between consecutive items that starts a new cluster")
	momentsCmd.Flags().Duration("max-span", 5*24*time.Hour, "Drop clusters whose total duration exceeds this")
	momentsCmd.Flags().Duration("window", 90*24*time.Hour, "How far back to scan (ignored when --all is set)")
	momentsCmd.Flags().Bool("all", false, "Scan the whole stash instead of the recent window")
	momentsCmd.Flags().Int("min-items", 3, "Smallest cluster size to surface")
	momentsCmd.Flags().Int("limit", 20, "Maximum number of suggestions to emit (highest-scored first)")
	momentsCmd.Flags().Bool("include-dismissed", false, "Don't filter out clusters the user has dismissed")

	momentsAcceptCmd.Flags().StringP("name", "n", "", "Collection name (required)")
	momentsAcceptCmd.Flags().StringP("description", "d", "", "Optional collection description")
	_ = momentsAcceptCmd.MarkFlagRequired("name")

	momentsCmd.AddCommand(momentsAcceptCmd)
	momentsCmd.AddCommand(momentsDismissCmd)
	momentsCmd.AddCommand(momentsUndismissCmd)
	momentsCmd.AddCommand(momentsDismissedCmd)
	rootCmd.AddCommand(momentsCmd)
}

var momentsDismissCmd = &cobra.Command{
	Use:   "dismiss ID...",
	Short: "Hide a Moment suggestion from future runs",
	Long: `Mark a cluster as user-rejected so it stops appearing in
subsequent `+"`stash moments`"+` output. Provide every item ID in the
cluster — the signature is the SHA-256 of the sorted ID set, so it
only matches that EXACT cluster shape. If the cluster later changes
(items added / removed), the new shape gets a new signature and
re-surfaces — that's intentional, so a dismissal doesn't permanently
hide every overlapping cluster.

Show what's currently hidden with `+"`stash moments dismissed`"+`;
un-hide one with `+"`stash moments undismiss <signature>`"+`.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMomentsDismiss,
}

var momentsUndismissCmd = &cobra.Command{
	Use:   "undismiss SIGNATURE",
	Short: "Re-surface a previously-dismissed Moment",
	Args:  cobra.ExactArgs(1),
	RunE:  runMomentsUndismiss,
}

var momentsDismissedCmd = &cobra.Command{
	Use:   "dismissed",
	Short: "List Moments the user has dismissed",
	RunE:  runMomentsDismissed,
}

func runMoments(cmd *cobra.Command, _ []string) error {
	maxGap, _ := cmd.Flags().GetDuration("max-gap")
	maxSpan, _ := cmd.Flags().GetDuration("max-span")
	window, _ := cmd.Flags().GetDuration("window")
	scanAll, _ := cmd.Flags().GetBool("all")
	minItems, _ := cmd.Flags().GetInt("min-items")
	limit, _ := cmd.Flags().GetInt("limit")
	includeDismissed, _ := cmd.Flags().GetBool("include-dismissed")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	filter := model.ItemFilter{}
	if !scanAll {
		after := time.Now().UTC().Add(-window)
		filter.After = &after
	}
	items, err := s.ListItems(ctx, filter)
	if err != nil {
		return err
	}
	dismissed, err := s.DismissedMomentSignatures(ctx)
	if err != nil {
		return fmt.Errorf("load dismissed moments: %w", err)
	}

	suggestions := buildSuggestions(items, momentParams{
		MaxGap:              maxGap,
		MaxSpan:             maxSpan,
		MinItems:            minItems,
		DismissedSignatures: dismissed,
		IncludeDismissed:    includeDismissed,
	})
	// Sort by score descending, then start time descending so the
	// most-recent / strongest trips bubble to the top.
	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].Score != suggestions[j].Score {
			return suggestions[i].Score > suggestions[j].Score
		}
		return suggestions[i].Start.After(suggestions[j].Start)
	})
	if limit > 0 && len(suggestions) > limit {
		suggestions = suggestions[:limit]
	}

	if flagJSON {
		printJSONSlice(suggestions)
		return nil
	}
	if len(suggestions) == 0 {
		fmt.Println("No trip suggestions in the current window.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SCORE\tITEMS\tWHEN\tSUGGESTED NAME\tIDS")
	for _, sug := range suggestions {
		fmt.Fprintf(w, "%.1f\t%d\t%s\t%s\t%s\n",
			sug.Score,
			sug.ItemCount,
			formatRange(sug.Start, sug.End),
			sug.SuggestedName,
			joinShortItemIDs(sug.Items),
		)
	}
	return w.Flush()
}

func runMomentsAccept(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	desc, _ := cmd.Flags().GetString("description")
	if name == "" {
		return fmt.Errorf("--name is required")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Idempotent — if the collection already exists, reuse it
	// rather than failing the whole accept on the second run.
	col, err := s.GetCollection(ctx, name)
	if err != nil || col == nil {
		col, err = s.CreateCollection(ctx, name, desc)
		if err != nil {
			return fmt.Errorf("create collection: %w", err)
		}
	}

	var added, skipped int
	for _, id := range args {
		if err := s.AddToCollection(ctx, id, col.Name); err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", shortID(id), err)
			skipped++
			continue
		}
		added++
	}
	if flagJSON {
		printJSON(map[string]any{
			"collection": col.Name,
			"added":      added,
			"skipped":    skipped,
		})
		return nil
	}
	fmt.Printf("✓ Collection %q now has %d new items (%d skipped).\n", col.Name, added, skipped)
	return nil
}

func runMomentsDismiss(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Reuse the same signature function the suggestion engine
	// uses so dismissals computed via this CLI path match what
	// the Mac UI would produce.
	previews := make([]MomentItemPreview, len(args))
	for i, id := range args {
		previews[i] = MomentItemPreview{ID: id}
	}
	signature := momentSignature(previews)

	// Best-effort: grab the first item's title so the
	// dismissed-list UI has something human to display. Errors
	// here are not fatal — we'd rather record the dismissal with
	// an empty title than fail when one of the IDs has been
	// archived.
	var sampleTitle string
	if first, ferr := s.GetItem(ctx, args[0]); ferr == nil && first != nil {
		sampleTitle = first.Title
	}

	if err := s.DismissMoment(ctx, signature, len(args), sampleTitle); err != nil {
		return fmt.Errorf("dismiss: %w", err)
	}
	if flagJSON {
		printJSON(map[string]any{
			"signature":    signature,
			"item_count":   len(args),
			"sample_title": sampleTitle,
		})
		return nil
	}
	fmt.Printf("✓ Dismissed Moment %s (%d items, sample: %s)\n",
		signature[:12], len(args), sampleTitle)
	return nil
}

func runMomentsUndismiss(cmd *cobra.Command, args []string) error {
	signature := args[0]
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.UndismissMoment(context.Background(), signature); err != nil {
		return fmt.Errorf("undismiss: %w", err)
	}
	if flagJSON {
		printJSON(map[string]any{"signature": signature, "undismissed": true})
		return nil
	}
	fmt.Printf("✓ Un-dismissed Moment %s\n", signature[:min(12, len(signature))])
	return nil
}

func runMomentsDismissed(cmd *cobra.Command, _ []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	rows, err := s.ListDismissedMoments(context.Background())
	if err != nil {
		return err
	}
	if flagJSON {
		printJSONSlice(rows)
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("No Moments are currently dismissed.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SIGNATURE\tITEMS\tDISMISSED AT\tSAMPLE TITLE")
	for _, dm := range rows {
		short := dm.Signature
		if len(short) > 16 {
			short = short[:16]
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\n",
			short,
			dm.ItemCount,
			dm.DismissedAt.Local().Format("2006-01-02 15:04"),
			strings.TrimSpace(dm.SampleTitle),
		)
	}
	return w.Flush()
}

// ───────────────────────────────────────────────────────────
// Pure suggestion engine (tested standalone)
// ───────────────────────────────────────────────────────────

// MomentSuggestion is the unit returned by `stash moments`. JSON
// shape is the contract for the Mac UI / scripts.
//
// `Items` carries enough per-item context (id, title, thumbnail path,
// type) for a UI to render a filmstrip preview without a second
// round trip per item. Callers needing only the ID list (e.g. piping
// into `accept`) can map across this slice. `Signature` is the
// stable cluster-identity hash used by the dismissal table — pass
// it to `stash moments undismiss <signature>` to surface a cluster
// the user previously hid.
type MomentSuggestion struct {
	Start          time.Time           `json:"start"`
	End            time.Time           `json:"end"`
	ItemCount      int                 `json:"item_count"`
	Items          []MomentItemPreview `json:"items"`
	SuggestedName  string              `json:"suggested_name"`
	Score          float64             `json:"score"`
	SharedTags     []string            `json:"shared_tags,omitempty"`
	LocationCenter *model.Location     `json:"location_center,omitempty"`
	LocationCount  int                 `json:"location_count,omitempty"`
	Signature      string              `json:"signature"`
}

// MomentItemPreview is the minimal subset of an item the trip-suggest
// UI needs to draw a filmstrip and let the user verify the cluster
// before accepting. ThumbnailPath is relative to the files dir (same
// convention as Item.ThumbnailPath); StorePath is the content-hashed
// blob path used by the UI as a fallback when a thumbnail hasn't been
// generated yet — for image items, rendering the full blob looks
// fine at small tile sizes and avoids a "🖼️ everywhere" placeholder
// fog on older captures that pre-date thumbnail-backfill.
type MomentItemPreview struct {
	ID            string `json:"id"`
	Title         string `json:"title,omitempty"`
	Type          string `json:"type,omitempty"`
	ThumbnailPath string `json:"thumbnail_path,omitempty"`
	StorePath     string `json:"store_path,omitempty"`
}

type momentParams struct {
	MaxGap   time.Duration
	MaxSpan  time.Duration
	MinItems int
	// DismissedSignatures lists clusters the user has explicitly
	// hidden — keyed by momentSignature. nil means "no filter."
	// Populated by the cobra wrapper from the store; the pure
	// pipeline stays trivially testable by passing a custom set.
	DismissedSignatures map[string]bool
	// IncludeDismissed flips off the dismissed filter regardless
	// of DismissedSignatures — backs the `--include-dismissed`
	// flag for inspecting the full set.
	IncludeDismissed bool
}

// buildSuggestions is the pure pipeline: items → clusters → filtered
// suggestions. Split from the cobra wrapper so it's table-testable.
func buildSuggestions(items []model.Item, p momentParams) []MomentSuggestion {
	clusters := clusterByTime(items, p.MaxGap)
	out := make([]MomentSuggestion, 0, len(clusters))
	for _, c := range clusters {
		if len(c) < p.MinItems {
			continue
		}
		span := c[len(c)-1].CreatedAt.Sub(c[0].CreatedAt)
		if p.MaxSpan > 0 && span > p.MaxSpan {
			continue
		}
		if allInSameCollection(c) {
			// Already grouped — don't re-suggest. Users who
			// want to revisit can drop the collection or use
			// --all.
			continue
		}
		sug := scoreCluster(c)
		if !p.IncludeDismissed && p.DismissedSignatures[sug.Signature] {
			continue
		}
		out = append(out, sug)
	}
	return out
}

// momentSignature returns the stable cluster identity backing
// dismissals: SHA-256 of the items' sorted IDs. Adding or removing
// an item from a cluster produces a different signature, so a
// dismissed cluster naturally re-surfaces in modified form rather
// than staying hidden forever.
func momentSignature(items []MomentItemPreview) string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	sort.Strings(ids)
	h := sha256.New()
	for i, id := range ids {
		if i > 0 {
			h.Write([]byte{'\n'})
		}
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// clusterByTime walks items in chronological order and starts a new
// cluster whenever the gap between consecutive items exceeds maxGap.
// Uses each item's CapturedAt when set (when the real-world content
// was created — EXIF shutter time, email send time, file mtime),
// falling back to CreatedAt (when the item landed in the stash).
// The fallback matters for URL items and any pre-captured_at-rollout
// content that hasn't been backfilled yet.
func clusterByTime(items []model.Item, maxGap time.Duration) [][]model.Item {
	if len(items) == 0 {
		return nil
	}
	sorted := make([]model.Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return clusterTime(&sorted[i]).Before(clusterTime(&sorted[j]))
	})
	var clusters [][]model.Item
	var current []model.Item
	var lastTime time.Time
	for _, item := range sorted {
		ct := clusterTime(&item)
		if len(current) == 0 || ct.Sub(lastTime) <= maxGap {
			current = append(current, item)
		} else {
			clusters = append(clusters, current)
			current = []model.Item{item}
		}
		lastTime = ct
	}
	if len(current) > 0 {
		clusters = append(clusters, current)
	}
	return clusters
}

// clusterTime returns the timestamp the clustering algorithm should
// use for an item — CapturedAt when populated, CreatedAt otherwise.
// Exposed so scoreCluster can reuse the same precedence when
// computing the suggestion's Start / End range.
func clusterTime(item *model.Item) time.Time {
	if item.CapturedAt != nil {
		return *item.CapturedAt
	}
	return item.CreatedAt
}

func scoreCluster(c []model.Item) MomentSuggestion {
	s := MomentSuggestion{
		Start:     clusterTime(&c[0]),
		End:       clusterTime(&c[len(c)-1]),
		ItemCount: len(c),
		Items:     make([]MomentItemPreview, 0, len(c)),
	}
	for _, it := range c {
		s.Items = append(s.Items, MomentItemPreview{
			ID:            it.ID,
			Title:         it.Title,
			Type:          string(it.Type),
			ThumbnailPath: it.ThumbnailPath,
			StorePath:     it.StorePath,
		})
	}
	s.Signature = momentSignature(s.Items)
	s.SharedTags = computeSharedTags(c)
	if center, count := computeLocationCenter(c); count > 0 {
		loc := center
		s.LocationCenter = &loc
		s.LocationCount = count
	}
	// Score components — tuned so size dominates but coherence
	// signals (shared tags + GPS) clearly separate weak and strong
	// clusters of the same size.
	score := float64(len(c))
	score += 2 * float64(len(s.SharedTags))
	if s.LocationCount > 0 {
		// +3 for "has location at all", +0.5 per item with
		// location (caps the bonus around a 6-item cluster).
		score += 3 + float64(s.LocationCount)*0.5
	}
	s.Score = score
	s.SuggestedName = nameFor(s)
	return s
}

// computeSharedTags returns tags that appear in at least 75% of the
// cluster (or all items, whichever is larger). Lower thresholds let
// one accidental tag dominate small clusters; higher thresholds drop
// useful signals on noisy ones.
func computeSharedTags(c []model.Item) []string {
	if len(c) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, it := range c {
		seen := make(map[string]bool)
		for _, t := range it.Tags {
			// Guard against duplicate-tag rows in the input.
			if seen[t.Name] {
				continue
			}
			seen[t.Name] = true
			counts[t.Name]++
		}
	}
	threshold := (len(c) * 3) / 4
	if threshold < 2 {
		threshold = 2
	}
	var out []string
	for name, cnt := range counts {
		if cnt >= threshold {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// computeLocationCenter returns the unweighted mean of all GPS-tagged
// items in the cluster, plus the count of items contributing. Caller
// uses count > 0 as the gate for "this cluster has location signal."
// Simple mean is fine for tight trip clusters (kilometers, not
// transcontinental); a real centroid is overkill.
func computeLocationCenter(c []model.Item) (model.Location, int) {
	var sumLat, sumLon float64
	var count int
	for _, it := range c {
		if it.Location == nil {
			continue
		}
		sumLat += it.Location.Lat
		sumLon += it.Location.Lon
		count++
	}
	if count == 0 {
		return model.Location{}, 0
	}
	return model.Location{
		Lat: sumLat / float64(count),
		Lon: sumLon / float64(count),
	}, count
}

// allInSameCollection returns true iff every item belongs to at
// least one collection AND there's at least one collection name that
// contains every item. Used to drop clusters the user has already
// curated so we don't keep re-suggesting them.
func allInSameCollection(c []model.Item) bool {
	if len(c) == 0 {
		return false
	}
	if len(c[0].Collections) == 0 {
		return false
	}
	// Start with the first item's collection set; intersect with
	// each subsequent item's set. Non-empty result ⇒ they share a
	// collection.
	candidate := make(map[string]bool)
	for _, col := range c[0].Collections {
		candidate[col.Name] = true
	}
	for _, it := range c[1:] {
		next := make(map[string]bool)
		for _, col := range it.Collections {
			if candidate[col.Name] {
				next[col.Name] = true
			}
		}
		candidate = next
		if len(candidate) == 0 {
			return false
		}
	}
	return len(candidate) > 0
}

func nameFor(s MomentSuggestion) string {
	startDay := s.Start.Format("2006-01-02")
	endDay := s.End.Format("2006-01-02")
	var datePart string
	switch {
	case startDay == endDay:
		datePart = startDay
	case s.Start.Year() == s.End.Year() && s.Start.Month() == s.End.Month():
		datePart = fmt.Sprintf("%s → %02d", startDay, s.End.Day())
	default:
		datePart = fmt.Sprintf("%s → %s", startDay, endDay)
	}
	if len(s.SharedTags) > 0 {
		return fmt.Sprintf("%s — %s", s.SharedTags[0], datePart)
	}
	return datePart
}

func formatRange(start, end time.Time) string {
	startDay := start.Format("2006-01-02 15:04")
	endDay := end.Format("2006-01-02 15:04")
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return fmt.Sprintf("%s → %s", startDay, end.Format("15:04"))
	}
	return fmt.Sprintf("%s → %s", startDay, endDay)
}

func joinShortItemIDs(items []MomentItemPreview) string {
	short := make([]string, len(items))
	for i, it := range items {
		short[i] = shortID(it.ID)
	}
	if len(short) > 4 {
		return fmt.Sprintf("%s … (+%d)",
			joinStrings(short[:3], " "),
			len(short)-3,
		)
	}
	return joinStrings(short, " ")
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// Compile-time guard: ensure the store satisfies the subset of
// methods this command uses. Cheap protection against an interface
// rename rippling without us noticing here.
var _ interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
	GetCollection(context.Context, string) (*model.Collection, error)
	CreateCollection(context.Context, string, string) (*model.Collection, error)
	AddToCollection(context.Context, string, string) error
} = (store.Store)(nil)
