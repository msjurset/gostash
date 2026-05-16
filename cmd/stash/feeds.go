package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurset/gostash/internal/feeds"
	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var feedsCmd = &cobra.Command{
	Use:   "feeds",
	Short: "Manage watched feed sources and their candidate inbox",
	Long: `Subscribe to RSS / Atom / RDF feeds — YouTube channels, Substacks,
blogs, subreddit JSON, arxiv keyword searches, anything emitting a
feed. The poller (stash feeds refresh) pulls new entries into the
triage inbox; the Mac app's Inbox scene + this CLI both expose them
for stash/dismiss/snooze.

Phase 1 implements the 'rss' kind, which works for RSS 2.0, Atom 1.0,
and RDF (RSS 1.0). Other kinds ('youtube' with channel-URL
auto-derive, etc.) will share the same parser later.`,
}

var feedsAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Add a new feed source",
	Args:  cobra.ExactArgs(2),
	RunE:  runFeedsAdd,
}

var feedsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List feed sources",
	RunE:  runFeedsList,
}

var feedsShowCmd = &cobra.Command{
	Use:   "show <id-or-name>",
	Short: "Show one feed source",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsShow,
}

var feedsEditCmd = &cobra.Command{
	Use:   "edit <id-or-name>",
	Short: "Edit a feed source",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsEdit,
}

var feedsRemoveCmd = &cobra.Command{
	Use:   "remove <id-or-name>",
	Short: "Delete a feed source and its candidates",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsRemove,
}

var feedsRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Poll feeds and ingest new candidates",
	Long: `Fetch each enabled feed source, diff against the existing
candidate set, and insert new entries as 'unread'. Sources flagged
auto_stash=true bypass the inbox and stash directly with their
per-source default tags and collection. Snoozed candidates whose
snooze_until has passed are flipped back to 'unread' on every
refresh, so the inbox surfaces them again without a separate cron.

With --source, only that one is polled. Designed for both ad-hoc CLI
use and as the implementation behind the Mac app's in-app timer / a
Runbook task.`,
	RunE: runFeedsRefresh,
}

var feedsCandidatesCmd = &cobra.Command{
	Use:     "candidates",
	Aliases: []string{"inbox"},
	Short:   "List candidates awaiting triage",
	RunE:    runFeedsCandidates,
}

var feedsDismissCmd = &cobra.Command{
	Use:   "dismiss <candidate-id>",
	Short: "Dismiss a candidate (won't re-surface)",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsDismiss,
}

var feedsSnoozeCmd = &cobra.Command{
	Use:   "snooze <candidate-id>",
	Short: "Snooze a candidate (re-surface after duration)",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsSnooze,
}

var feedsStashCmd = &cobra.Command{
	Use:   "stash <candidate-id>",
	Short: "Stash a candidate as a new item, applying source defaults",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeedsStash,
}

var feedsRewriteNotesCmd = &cobra.Command{
	Use:   "rewrite-notes",
	Short: "Convert raw-HTML notes on stashed feed items to Markdown",
	Long: `Walk items previously stashed from feed candidates and run their notes
through the HTML → Markdown converter. Idempotent — items whose notes
are already clean Markdown pass through unchanged.

Default scope is "items that came from feed candidates" (joined via
feed_candidates.stashed_item_id). Pass --all to also scan items
captured by other paths in case some of those got raw HTML notes too.`,
	RunE: runFeedsRewriteNotes,
}

var feedsReconvertCmd = &cobra.Command{
	Use:   "reconvert",
	Short: "Populate description_markdown on candidates that pre-date the cache",
	Long: `Walk every feed candidate row, run its HTML description through the
converter, and write the result to description_markdown. Used after
upgrading the converter or to back-fill rows that were captured
before the cache column existed. Idempotent.`,
	RunE: runFeedsReconvert,
}

func init() {
	feedsAddCmd.Flags().String("kind", "rss", "Feed kind (rss | youtube). 'youtube' accepts a channel URL and resolves it to the RSS feed.")
	feedsAddCmd.Flags().StringSliceP("tag", "T", nil, "Default tag(s) applied at stash time")
	feedsAddCmd.Flags().StringP("collection", "c", "", "Default collection")
	feedsAddCmd.Flags().Bool("auto-stash", false, "Auto-stash new candidates without triage")
	feedsAddCmd.Flags().Bool("fetch-content", false, "After polling, fetch each candidate's article URL and extract full content (use for thin-description feeds like Hacker News)")
	feedsAddCmd.Flags().Int("interval", 360, "Poll interval in minutes")
	feedsAddCmd.Flags().Bool("disabled", false, "Start disabled (won't be polled)")

	feedsEditCmd.Flags().String("name", "", "Rename")
	feedsEditCmd.Flags().String("url", "", "Change feed URL")
	feedsEditCmd.Flags().String("kind", "", "Change kind")
	feedsEditCmd.Flags().StringSliceP("tag", "T", nil, "Replace default tag(s)")
	feedsEditCmd.Flags().Bool("clear-tags", false, "Clear default tags")
	feedsEditCmd.Flags().String("collection", "", "Change default collection")
	feedsEditCmd.Flags().Bool("clear-collection", false, "Clear default collection")
	feedsEditCmd.Flags().Bool("auto-stash", false, "Set auto-stash on")
	feedsEditCmd.Flags().Bool("no-auto-stash", false, "Set auto-stash off")
	feedsEditCmd.Flags().Bool("fetch-content", false, "Enable per-candidate article-content fetching")
	feedsEditCmd.Flags().Bool("no-fetch-content", false, "Disable per-candidate article-content fetching")
	feedsEditCmd.Flags().Int("interval", 0, "Change poll interval (minutes)")
	feedsEditCmd.Flags().Bool("enable", false, "Enable polling")
	feedsEditCmd.Flags().Bool("disable", false, "Disable polling")

	feedsRefreshCmd.Flags().String("source", "", "Only refresh this source (id or name)")

	feedsCandidatesCmd.Flags().String("state", "unread", "State filter: unread,stashed,dismissed,snoozed (comma-separated)")
	feedsCandidatesCmd.Flags().String("source", "", "Only show candidates from this source (id or name)")
	feedsCandidatesCmd.Flags().IntP("limit", "l", 50, "Maximum rows (0 = all)")

	feedsSnoozeCmd.Flags().Duration("for", 24*time.Hour, "Duration to snooze")

	feedsStashCmd.Flags().StringSliceP("tag", "T", nil, "Additional tag(s) (combined with source defaults)")
	feedsStashCmd.Flags().StringP("collection", "c", "", "Override default collection")
	feedsStashCmd.Flags().StringP("notes", "n", "", "Notes")

	feedsRewriteNotesCmd.Flags().Bool("all", false, "Scan every item, not just those linked from feed_candidates")
	feedsRewriteNotesCmd.Flags().Bool("dry-run", false, "Report changes without saving")

	feedsReconvertCmd.Flags().Bool("dry-run", false, "Report changes without saving")

	feedsCmd.AddCommand(feedsAddCmd, feedsListCmd, feedsShowCmd, feedsEditCmd, feedsRemoveCmd,
		feedsRefreshCmd, feedsCandidatesCmd, feedsDismissCmd, feedsSnoozeCmd, feedsStashCmd,
		feedsRewriteNotesCmd, feedsReconvertCmd)
	rootCmd.AddCommand(feedsCmd)
}

func runFeedsAdd(cmd *cobra.Command, args []string) error {
	kind, _ := cmd.Flags().GetString("kind")
	tags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	autoStash, _ := cmd.Flags().GetBool("auto-stash")
	fetchContent, _ := cmd.Flags().GetBool("fetch-content")
	interval, _ := cmd.Flags().GetInt("interval")
	disabled, _ := cmd.Flags().GetBool("disabled")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	feedURL := args[1]
	// YouTube convenience: accept any flavor of channel URL and
	// rewrite to the videos.xml RSS endpoint that the parser
	// understands. The stored kind stays 'rss' since the underlying
	// feed is just an Atom feed once we get there.
	if kind == "youtube" {
		resolved, err := feeds.ResolveYouTubeFeed(feedURL)
		if err != nil {
			return fmt.Errorf("resolve youtube channel: %w", err)
		}
		feedURL = resolved
		kind = "rss"
	}

	src := &model.FeedSource{
		Name:                args[0],
		Kind:                kind,
		URL:                 feedURL,
		DefaultTags:         tags,
		DefaultCollection:   collection,
		AutoStash:           autoStash,
		FetchContent:        fetchContent,
		PollIntervalMinutes: interval,
		Enabled:             !disabled,
	}
	if err := s.CreateFeedSource(context.Background(), src); err != nil {
		return err
	}
	if flagJSON {
		printJSON(src)
		return nil
	}
	fmt.Printf("Added feed [%d] %s — %s\n", src.ID, src.Name, src.URL)
	return nil
}

func runFeedsList(_ *cobra.Command, _ []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	sources, err := s.ListFeedSources(context.Background(), false)
	if err != nil {
		return err
	}
	if flagJSON {
		printJSONSlice(sources)
		return nil
	}
	if len(sources) == 0 {
		fmt.Println("No feeds.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tKIND\tENABLED\tAUTO\tLAST POLLED\tURL")
	for _, src := range sources {
		last := "—"
		if src.LastPolledAt != nil {
			last = src.LastPolledAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%v\t%v\t%s\t%s\n",
			src.ID, src.Name, src.Kind, src.Enabled, src.AutoStash, last, src.URL)
	}
	return w.Flush()
}

func runFeedsShow(_ *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	src, err := s.GetFeedSource(context.Background(), args[0])
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(src)
		return nil
	}
	fmt.Printf("Feed %d: %s\n", src.ID, src.Name)
	fmt.Printf("  Kind:       %s\n", src.Kind)
	fmt.Printf("  URL:        %s\n", src.URL)
	fmt.Printf("  Enabled:    %v\n", src.Enabled)
	fmt.Printf("  Auto-stash: %v\n", src.AutoStash)
	fmt.Printf("  Interval:   %d min\n", src.PollIntervalMinutes)
	if len(src.DefaultTags) > 0 {
		fmt.Printf("  Tags:       %s\n", strings.Join(src.DefaultTags, ", "))
	}
	if src.DefaultCollection != "" {
		fmt.Printf("  Collection: %s\n", src.DefaultCollection)
	}
	if src.LastPolledAt != nil {
		fmt.Printf("  Polled:     %s\n", src.LastPolledAt.Local().Format("2006-01-02 15:04:05"))
	}
	if src.LastError != "" {
		fmt.Printf("  Error:      %s\n", src.LastError)
	}
	return nil
}

func runFeedsEdit(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	src, err := s.GetFeedSource(ctx, args[0])
	if err != nil {
		return err
	}
	if name, _ := cmd.Flags().GetString("name"); name != "" {
		src.Name = name
	}
	if url, _ := cmd.Flags().GetString("url"); url != "" {
		src.URL = url
	}
	if kind, _ := cmd.Flags().GetString("kind"); kind != "" {
		src.Kind = kind
	}
	if cmd.Flags().Changed("tag") {
		tags, _ := cmd.Flags().GetStringSlice("tag")
		src.DefaultTags = tags
	}
	if clear, _ := cmd.Flags().GetBool("clear-tags"); clear {
		src.DefaultTags = nil
	}
	if cmd.Flags().Changed("collection") {
		coll, _ := cmd.Flags().GetString("collection")
		src.DefaultCollection = coll
	}
	if clear, _ := cmd.Flags().GetBool("clear-collection"); clear {
		src.DefaultCollection = ""
	}
	if v, _ := cmd.Flags().GetBool("auto-stash"); v {
		src.AutoStash = true
	}
	if v, _ := cmd.Flags().GetBool("no-auto-stash"); v {
		src.AutoStash = false
	}
	if v, _ := cmd.Flags().GetBool("fetch-content"); v {
		src.FetchContent = true
	}
	if v, _ := cmd.Flags().GetBool("no-fetch-content"); v {
		src.FetchContent = false
	}
	if interval, _ := cmd.Flags().GetInt("interval"); interval > 0 {
		src.PollIntervalMinutes = interval
	}
	if v, _ := cmd.Flags().GetBool("enable"); v {
		src.Enabled = true
	}
	if v, _ := cmd.Flags().GetBool("disable"); v {
		src.Enabled = false
	}
	if err := s.UpdateFeedSource(ctx, src); err != nil {
		return err
	}
	if flagJSON {
		printJSON(src)
		return nil
	}
	fmt.Printf("Updated feed [%d] %s\n", src.ID, src.Name)
	return nil
}

func runFeedsRemove(_ *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.DeleteFeedSource(context.Background(), args[0]); err != nil {
		return err
	}
	if !flagJSON {
		fmt.Printf("Removed feed %s\n", args[0])
	}
	return nil
}

func runFeedsRefresh(cmd *cobra.Command, _ []string) error {
	target, _ := cmd.Flags().GetString("source")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	var sources []model.FeedSource
	if target != "" {
		src, err := s.GetFeedSource(ctx, target)
		if err != nil {
			return err
		}
		sources = []model.FeedSource{*src}
	} else {
		sources, err = s.ListFeedSources(ctx, true)
		if err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	expired, _ := s.ExpireSnoozedCandidates(ctx, now)

	type summary struct {
		Source      string `json:"source"`
		Found       int    `json:"found"`
		New         int    `json:"new"`
		Enriched    int    `json:"enriched,omitempty"`
		AutoStashed int    `json:"auto_stashed,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	out := make([]summary, 0, len(sources))

	for _, src := range sources {
		s.TouchFeedSourcePoll(ctx, src.ID, "")
		entries, err := feeds.Fetch(src.URL)
		if err != nil {
			s.TouchFeedSourcePoll(ctx, src.ID, err.Error())
			out = append(out, summary{Source: src.Name, Error: err.Error()})
			continue
		}
		sum := summary{Source: src.Name, Found: len(entries)}
		for _, e := range entries {
			// Pre-convert HTML → Markdown at poll time so the Mac
			// Inbox preview pane renders instantly. Cheap; the
			// description has already been downloaded.
			cand := model.FeedCandidate{
				SourceID:            src.ID,
				GUID:                e.GUID,
				URL:                 e.URL,
				Title:               e.Title,
				Description:         e.Description,
				DescriptionMarkdown: feeds.HTMLToMarkdown(e.Description),
				ThumbnailURL:        e.ThumbnailURL,
				PublishedAt:         e.PublishedAt,
			}
			created, err := s.UpsertFeedCandidate(ctx, &cand)
			if err != nil {
				out = append(out, summary{Source: src.Name, Error: err.Error()})
				continue
			}
			if !created {
				continue
			}
			sum.New++
			// Per-source article-content fetch. Best-effort: a fetch
			// failure leaves the original RSS description in place
			// rather than aborting the poll. Run BEFORE auto-stash
			// so a stashed candidate inherits the enriched notes.
			if src.FetchContent && cand.URL != "" {
				if err := enrichCandidateContent(ctx, s, &cand); err == nil {
					sum.Enriched++
				}
			}
			if src.AutoStash {
				if _, err := autoStashCandidate(ctx, s, fs, &src, &cand); err == nil {
					sum.AutoStashed++
				}
			}
		}
		out = append(out, sum)
	}

	if flagJSON {
		printJSON(map[string]any{
			"sources":         out,
			"snoozed_expired": expired,
		})
		return nil
	}
	for _, s := range out {
		if s.Error != "" {
			fmt.Printf("  %s — error: %s\n", s.Source, s.Error)
			continue
		}
		extra := ""
		if s.Enriched > 0 {
			extra += fmt.Sprintf(", enriched %d", s.Enriched)
		}
		if s.AutoStashed > 0 {
			extra += fmt.Sprintf(", auto-stashed %d", s.AutoStashed)
		}
		fmt.Printf("  %s — %d new (%d found%s)\n", s.Source, s.New, s.Found, extra)
	}
	if expired > 0 {
		fmt.Printf("  %d snoozed candidate(s) returned to inbox\n", expired)
	}
	return nil
}

func runFeedsCandidates(cmd *cobra.Command, _ []string) error {
	stateFlag, _ := cmd.Flags().GetString("state")
	sourceFlag, _ := cmd.Flags().GetString("source")
	limit, _ := cmd.Flags().GetInt("limit")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	filter := store.FeedCandidateFilter{Limit: limit}
	for _, st := range strings.Split(stateFlag, ",") {
		if st = strings.TrimSpace(st); st != "" {
			filter.States = append(filter.States, st)
		}
	}
	if sourceFlag != "" {
		src, err := s.GetFeedSource(ctx, sourceFlag)
		if err != nil {
			return err
		}
		filter.SourceID = src.ID
	}
	cands, err := s.ListFeedCandidates(ctx, filter)
	if err != nil {
		return err
	}
	if flagJSON {
		printJSONSlice(cands)
		return nil
	}
	if len(cands) == 0 {
		fmt.Println("No candidates.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tSOURCE\tWHEN\tTITLE")
	for _, c := range cands {
		when := "—"
		if c.PublishedAt != nil {
			when = c.PublishedAt.Local().Format("2006-01-02 15:04")
		} else {
			when = c.DiscoveredAt.Local().Format("2006-01-02 15:04")
		}
		title := c.Title
		if title == "" {
			title = c.URL
		}
		if len(title) > 70 {
			title = title[:67] + "…"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", c.ID, c.State, c.SourceName, when, title)
	}
	return w.Flush()
}

func runFeedsDismiss(_ *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("candidate id must be numeric: %v", err)
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.UpdateFeedCandidateState(context.Background(), id, model.FeedStateDismissed, nil, ""); err != nil {
		return err
	}
	if !flagJSON {
		fmt.Printf("Dismissed candidate %d\n", id)
	}
	return nil
}

func runFeedsSnooze(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("candidate id must be numeric: %v", err)
	}
	dur, _ := cmd.Flags().GetDuration("for")
	until := time.Now().UTC().Add(dur)
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.UpdateFeedCandidateState(context.Background(), id, model.FeedStateSnoozed, &until, ""); err != nil {
		return err
	}
	if !flagJSON {
		fmt.Printf("Snoozed candidate %d until %s\n", id, until.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func runFeedsStash(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("candidate id must be numeric: %v", err)
	}
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collOverride, _ := cmd.Flags().GetString("collection")
	notes, _ := cmd.Flags().GetString("notes")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	cand, err := s.GetFeedCandidate(ctx, id)
	if err != nil {
		return err
	}
	src, err := s.GetFeedSource(ctx, strconv.FormatInt(cand.SourceID, 10))
	if err != nil {
		return err
	}
	item, err := stashCandidate(ctx, s, fs, src, cand, stashOptions{
		ExtraTags:  extraTags,
		Collection: collOverride,
		Notes:      notes,
	})
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(item)
		return nil
	}
	fmt.Printf("Stashed [%s] %s\n", shortID(item.ID), item.Title)
	return nil
}

// ───────────────────────────────────────────────────────────
// Shared stash-candidate path
// ───────────────────────────────────────────────────────────

type stashOptions struct {
	ExtraTags  []string
	Collection string
	Notes      string
}

// autoStashCandidate is what runFeedsRefresh calls when the source
// has auto_stash=true: skip triage, stash with source defaults.
func autoStashCandidate(ctx context.Context, s store.Store, fs interface{}, src *model.FeedSource, cand *model.FeedCandidate) (*model.Item, error) {
	return stashCandidate(ctx, s, fs, src, cand, stashOptions{})
}

// stashCandidate creates a new URL item from a feed candidate, applies
// per-source default tags + collection (combined with explicit
// overrides), links the candidate row to the new item, and flips the
// candidate state to 'stashed'. We don't pre-fetch the page content
// here — that's the existing `refresh` path's job and we want
// `stash feeds` to be cheap.
func stashCandidate(ctx context.Context, s store.Store, _ interface{}, src *model.FeedSource, cand *model.FeedCandidate, opts stashOptions) (*model.Item, error) {
	title := cand.Title
	if title == "" {
		title = cand.URL
	}
	tags := mergeTagSets(src.DefaultTags, opts.ExtraTags)
	collection := opts.Collection
	if collection == "" {
		collection = src.DefaultCollection
	}
	notes := opts.Notes
	if notes == "" {
		// Prefer the cached Markdown from poll time; legacy rows
		// (captured before the description_markdown column was
		// populated) fall back to converting inline.
		if cand.DescriptionMarkdown != "" {
			notes = cand.DescriptionMarkdown
		} else {
			notes = feeds.HTMLToMarkdown(cand.Description)
		}
	}
	now := time.Now().UTC()
	item := &model.Item{
		ID:        newFetchID(),
		Type:      model.TypeURL,
		Title:     title,
		URL:       cand.URL,
		Notes:     notes,
		Metadata:  json.RawMessage("{}"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, t := range tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}
	if err := s.CreateItem(ctx, item); err != nil {
		return nil, fmt.Errorf("create item: %w", err)
	}
	if err := s.UpdateFeedCandidateState(ctx, cand.ID, model.FeedStateStashed, nil, item.ID); err != nil {
		return item, fmt.Errorf("mark candidate stashed: %w", err)
	}
	return item, nil
}

func runFeedsRewriteNotes(cmd *cobra.Command, _ []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	targets, err := collectRewriteTargets(ctx, s, all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if !flagJSON {
			fmt.Println("No items to scan.")
		}
		return nil
	}

	type change struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var changed []change
	scanned := 0
	for _, item := range targets {
		scanned++
		if item.Notes == "" {
			continue
		}
		clean := feeds.HTMLToMarkdown(item.Notes)
		if clean == item.Notes {
			continue
		}
		changed = append(changed, change{ID: item.ID, Title: item.Title})
		if dryRun {
			continue
		}
		updated := item
		updated.Notes = clean
		if err := s.UpdateItem(ctx, &updated); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [%s] %v\n", shortID(item.ID), err)
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"scanned":   scanned,
			"changed":   len(changed),
			"dry_run":   dryRun,
			"items":     changed,
		})
		return nil
	}
	verb := "Updated"
	if dryRun {
		verb = "Would update"
	}
	for _, c := range changed {
		fmt.Printf("  ✓ [%s] %s\n", shortID(c.ID), c.Title)
	}
	fmt.Printf("\nScanned %d item(s); %s %d\n", scanned, verb, len(changed))
	return nil
}

func runFeedsReconvert(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Walk every candidate (any state) — the markdown cache is
	// independent of the triage state machine.
	cands, err := s.ListFeedCandidates(ctx, store.FeedCandidateFilter{
		States: []string{model.FeedStateUnread, model.FeedStateStashed, model.FeedStateDismissed, model.FeedStateSnoozed},
		Limit:  0,
	})
	if err != nil {
		return err
	}

	type change struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	var changed []change
	for _, c := range cands {
		md := feeds.HTMLToMarkdown(c.Description)
		if md == c.DescriptionMarkdown {
			continue
		}
		changed = append(changed, change{ID: c.ID, Title: c.Title})
		if dryRun {
			continue
		}
		if err := s.UpdateFeedCandidateMarkdown(ctx, c.ID, md); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ✗ [%d] %v\n", c.ID, err)
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"scanned": len(cands),
			"changed": len(changed),
			"dry_run": dryRun,
			"items":   changed,
		})
		return nil
	}
	verb := "Updated"
	if dryRun {
		verb = "Would update"
	}
	for _, c := range changed {
		fmt.Printf("  ✓ [%d] %s\n", c.ID, c.Title)
	}
	fmt.Printf("\nScanned %d; %s %d\n", len(cands), verb, len(changed))
	return nil
}

// collectRewriteTargets returns the set of items whose notes the
// rewrite-notes command should examine. Default (all=false) limits
// to items linked from feed_candidates.stashed_item_id so we don't
// risk rewriting notes the user typed by hand. all=true broadens to
// every item in the stash.
func collectRewriteTargets(ctx context.Context, s store.Store, all bool) ([]model.Item, error) {
	if all {
		// Listing with limit=0 returns every item — note that the
		// CLI's `list` defaults to a small page; we explicitly ask
		// for unlimited here.
		return s.ListItems(ctx, model.ItemFilter{Limit: 0, IncludeArchived: true})
	}
	// Source-linked: walk every stashed candidate and resolve its
	// item. Dedup by id in case multiple candidates point at the
	// same item (shouldn't happen, but cheap to guard).
	cands, err := s.ListFeedCandidates(ctx, store.FeedCandidateFilter{States: []string{model.FeedStateStashed}, Limit: 0})
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]model.Item, 0, len(cands))
	for _, c := range cands {
		if c.StashedItemID == "" {
			continue
		}
		if _, dup := seen[c.StashedItemID]; dup {
			continue
		}
		seen[c.StashedItemID] = struct{}{}
		item, err := s.GetItem(ctx, c.StashedItemID)
		if err != nil {
			continue // item may have been deleted; skip
		}
		out = append(out, *item)
	}
	return out, nil
}

// enrichCandidateContent fetches the candidate's article URL via the
// readability pipeline and overwrites the candidate's description +
// description_markdown with the extracted content. Best-effort: any
// failure (404, paywalled, non-HTML response, extraction empty) leaves
// the RSS description in place and returns an error the caller can
// silently swallow. Also mutates the in-memory candidate so a
// subsequent `autoStashCandidate` call lands the enriched notes.
func enrichCandidateContent(ctx context.Context, s store.Store, cand *model.FeedCandidate) error {
	result, err := fetch.URL(cand.URL)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(result.ExtractedText)
	if body == "" {
		return fmt.Errorf("empty extraction")
	}
	// The readability output is already Markdown (see fetch.go) so
	// we can store it in both columns directly — description_markdown
	// is what the Mac inbox renders, description is the canonical
	// form used by HTMLToMarkdown on legacy back-fill.
	if err := s.UpdateFeedCandidateContent(ctx, cand.ID, body, body); err != nil {
		return err
	}
	cand.Description = body
	cand.DescriptionMarkdown = body
	return nil
}

func mergeTagSets(a, b []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, set := range [][]string{a, b} {
		for _, t := range set {
			tt := strings.TrimSpace(t)
			if tt == "" {
				continue
			}
			if _, dup := seen[tt]; dup {
				continue
			}
			seen[tt] = struct{}{}
			out = append(out, tt)
		}
	}
	return out
}
