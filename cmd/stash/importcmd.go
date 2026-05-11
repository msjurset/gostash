package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
	"golang.org/x/net/html"
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Import items from external sources",
}

var importBookmarksCmd = &cobra.Command{
	Use:   "bookmarks <file>",
	Short: "Import bookmarks from Chrome/Firefox HTML export",
	Long: `Import bookmarks from a Netscape-format HTML bookmark file.
Chrome: chrome://bookmarks → ⋮ → Export bookmarks
Firefox: Ctrl+Shift+O → Import and Backup → Export Bookmarks to HTML

Bookmark folders are converted to tags. Duplicate URLs are skipped.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportBookmarks,
}

var importPocketCmd = &cobra.Command{
	Use:   "pocket <file>",
	Short: "Import bookmarks from Pocket HTML export",
	Long: `Import bookmarks from a Pocket export file.
Export from: getpocket.com/export

Tags from Pocket are preserved. The time_added attribute sets the item's creation date.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportBookmarks, // Same Netscape HTML format
}

func init() {
	for _, cmd := range []*cobra.Command{importBookmarksCmd, importPocketCmd} {
		cmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
		cmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
		cmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	}
	importBackfillCmd.Flags().IntP("limit", "l", 50, "Max items to process")
	importCmd.AddCommand(importBookmarksCmd)
	importCmd.AddCommand(importPocketCmd)
	importCmd.AddCommand(importBackfillCmd)
	importCmd.AddCommand(importApplyCmd)
	rootCmd.AddCommand(importCmd)
}

// import apply — source-agnostic commit step. Reads a manifest of
// URL items from stdin and writes them to the store with the
// existing dedup-by-URL semantics. Paired with the `--dry-run --json`
// preview emitted by chrome/firefox/bookmarks subcommands.
//
// The Mac importer uses this to commit a user-curated subset of
// the preview tree (with possibly-edited per-item tags + a shared
// collection) in one round-trip.
var importApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Commit a manifest of URL items read from stdin",
	Long: `Read a JSON manifest from stdin and import each entry as a
URL item, deduping by URL against existing items. Pair with
` + "`stash import {chrome,firefox,bookmarks,pocket} --dry-run --json`" + ` to
preview, then submit a curated subset back through apply.

Manifest shape (stdin):

  {
    "collection": "Imported Bookmarks",   // optional
    "items": [
      {
        "url": "https://example.com",
        "title": "Example",
        "tags": ["work", "design"],
        "created_at": "2024-01-15T10:30:00Z",  // optional
        "notes": "..."                          // optional
      }
    ]
  }

Output mirrors stash import archive: { imported, skipped, errors }.`,
	RunE: runImportApply,
}

func runImportApply(cmd *cobra.Command, args []string) error {
	type manifestItem struct {
		URL       string   `json:"url"`
		Title     string   `json:"title"`
		Tags      []string `json:"tags,omitempty"`
		CreatedAt *string  `json:"created_at,omitempty"`
		Notes     string   `json:"notes,omitempty"`
	}
	type manifest struct {
		Collection string         `json:"collection,omitempty"`
		Items      []manifestItem `json:"items"`
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	var imported, skipped int
	var errs []string

	for _, mi := range m.Items {
		if mi.URL == "" {
			continue
		}
		exists, err := s.ExistsByURL(ctx, mi.URL)
		if err != nil {
			errs = append(errs, fmt.Sprintf("check %s: %v", mi.URL, err))
			continue
		}
		if exists {
			skipped++
			continue
		}

		now := time.Now().UTC()
		entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
		id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

		created := now
		if mi.CreatedAt != nil {
			if t, err := time.Parse(time.RFC3339, *mi.CreatedAt); err == nil {
				created = t
			}
		}

		title := mi.Title
		if title == "" {
			title = mi.URL
		}

		item := &model.Item{
			ID:        id,
			Type:      model.TypeURL,
			Title:     title,
			URL:       mi.URL,
			Notes:     mi.Notes,
			CreatedAt: created,
			UpdatedAt: now,
			Metadata:  json.RawMessage("{}"),
		}
		tagSet := make(map[string]bool)
		for _, t := range mi.Tags {
			t = strings.TrimSpace(t)
			if t != "" {
				tagSet[t] = true
			}
		}
		for t := range tagSet {
			item.Tags = append(item.Tags, model.Tag{Name: t})
		}
		if m.Collection != "" {
			item.Collections = append(item.Collections, model.Collection{Name: m.Collection})
		}
		if err := s.CreateItem(ctx, item); err != nil {
			errs = append(errs, fmt.Sprintf("create %s: %v", mi.URL, err))
			continue
		}
		imported++
		if !flagJSON {
			fmt.Printf("  Imported [%s] %s\n", shortID(id), title)
		}
	}

	if flagJSON {
		payload := map[string]any{
			"imported": imported,
			"skipped":  skipped,
			"total":    len(m.Items),
		}
		if len(errs) > 0 {
			payload["errors"] = errs
		}
		printJSON(payload)
	} else {
		fmt.Printf("\n%d imported, %d skipped, %d errors, %d total\n",
			imported, skipped, len(errs), len(m.Items))
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
	}
	return nil
}

var importBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Fetch content for URL items missing extracted text",
	Long:  `Finds URL items without extracted text and fetches their page content.`,
	RunE:  runImportBackfill,
}

func runImportBackfill(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	limit, _ := cmd.Flags().GetInt("limit")
	ctx := context.Background()

	items, err := s.ListURLsWithoutContent(ctx, limit)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		fmt.Println("No URL items need backfill.")
		return nil
	}

	fmt.Printf("Found %d URL items without content. Fetching...\n", len(items))

	var fetched, failed int
	for i, item := range items {
		fmt.Printf("  [%d/%d] %s ... ", i+1, len(items), truncate(item.Title, 50))

		result, err := fetch.URL(item.URL)
		if err != nil {
			fmt.Printf("failed: %v\n", err)
			failed++
			continue
		}

		item.ExtractedText = result.ExtractedText
		item.MimeType = result.MimeType
		if result.Title != "" && (item.Title == "" || item.Title == item.URL) {
			item.Title = result.Title
		}

		if err := s.UpdateItem(ctx, &item); err != nil {
			fmt.Printf("save failed: %v\n", err)
			failed++
			continue
		}

		fetched++
		fmt.Println("ok")
	}

	fmt.Printf("\nDone: %d fetched, %d failed, %d total\n", fetched, failed, len(items))
	return nil
}

type bookmark struct {
	url        string
	title      string
	tags       []string   // derived from folder path + inline tags
	folderPath []string   // raw folder breadcrumb (outermost → innermost); empty = top-level
	notes      string
	createdAt  *time.Time // from time_added attribute
}

func runImportBookmarks(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	bookmarks, err := parseBookmarksHTML(f)
	if err != nil {
		return fmt.Errorf("parse bookmarks: %w", err)
	}

	if dryRun {
		fmt.Printf("Found %d bookmarks. (dry run — nothing will be saved)\n\n", len(bookmarks))
		for _, bm := range bookmarks {
			tags := append(bm.tags, extraTags...)
			fmt.Printf("  %s\n    %s\n", bm.title, bm.url)
			if len(tags) > 0 {
				fmt.Printf("    tags: %s\n", strings.Join(tags, ", "))
			}
		}
		return nil
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	var imported, skipped int

	for _, bm := range bookmarks {
		// Dedup by URL
		exists, err := s.ExistsByURL(ctx, bm.url)
		if err != nil {
			return fmt.Errorf("check duplicate: %w", err)
		}
		if exists {
			skipped++
			continue
		}

		now := time.Now().UTC()
		entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
		id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

		created := now
		if bm.createdAt != nil {
			created = *bm.createdAt
		}

		item := &model.Item{
			ID:        id,
			Type:      model.TypeURL,
			Title:     bm.title,
			URL:       bm.url,
			Notes:     bm.notes,
			CreatedAt: created,
			UpdatedAt: now,
			Metadata:  json.RawMessage("{}"),
		}

		// Combine folder tags + extra tags, dedup
		allTags := make(map[string]bool)
		for _, t := range bm.tags {
			allTags[t] = true
		}
		for _, t := range extraTags {
			allTags[t] = true
		}
		for t := range allTags {
			item.Tags = append(item.Tags, model.Tag{Name: t})
		}

		if collection != "" {
			item.Collections = append(item.Collections, model.Collection{Name: collection})
		}

		if err := s.CreateItem(ctx, item); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to import %q: %v\n", bm.url, err)
			continue
		}
		imported++

		if !flagJSON {
			fmt.Printf("  Imported [%s] %s\n", shortID(id), bm.title)
		}
	}

	if flagJSON {
		printJSON(map[string]int{"imported": imported, "skipped": skipped, "total": len(bookmarks)})
	} else {
		fmt.Printf("\nDone: %d imported, %d skipped (duplicate), %d total\n", imported, skipped, len(bookmarks))
	}

	return nil
}

// parseBookmarksHTML parses a Netscape-format bookmarks HTML file.
// Folder names become tags on the bookmarks within them.
func parseBookmarksHTML(r io.Reader) ([]bookmark, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	var bookmarks []bookmark
	// Raw folder names (as they appeared in the HTML) and the
	// normalized-tag form of each. Kept parallel so we can both
	// preserve the original breadcrumb for the Mac tree UI and
	// derive the tag list cheaply.
	var folderRawStack []string
	var folderTagStack []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h3":
				// Folder heading — push folder name onto stack
				if text := extractText(n); text != "" {
					folderRawStack = append(folderRawStack, text)
					folderTagStack = append(folderTagStack, normalizeTag(text))
				}
			case "a":
				// Bookmark link
				href := getAttr(n, "href")
				if href != "" && (strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://")) {
					title := extractText(n)
					if title == "" {
						title = href
					}
					// Copy current folder stack as tags, skip empty
					var tags []string
					for _, t := range folderTagStack {
						if t != "" {
							tags = append(tags, t)
						}
					}
					// Inline tags from Pocket exports (comma-separated)
					if inlineTags := getAttr(n, "tags"); inlineTags != "" {
						for _, t := range strings.Split(inlineTags, ",") {
							t = strings.TrimSpace(t)
							if t != "" {
								tags = append(tags, normalizeTag(t))
							}
						}
					}
					// Preserve the raw folder breadcrumb so the Mac
					// importer's tree UI can render the original
					// hierarchy. `tags` is the normalized form.
					folderCopy := append([]string{}, folderRawStack...)
					bm := bookmark{
						url:        href,
						title:      title,
						tags:       tags,
						folderPath: folderCopy,
					}
					// time_added from Pocket exports (unix timestamp)
					if ts := getAttr(n, "time_added"); ts != "" {
						if sec, err := strconv.ParseInt(ts, 10, 64); err == nil {
							t := time.Unix(sec, 0).UTC()
							bm.createdAt = &t
						}
					}
					bookmarks = append(bookmarks, bm)
				}
			}
		}

		// Track DL nesting for folder boundaries
		if n.Type == html.ElementNode && n.Data == "dl" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			// Pop folder when leaving a DL (if we pushed one)
			// The structure is: <DT><H3>Folder</H3><DL>...items...</DL>
			// When we leave a DL, the folder scope ends
			if len(folderRawStack) > 0 {
				folderRawStack = folderRawStack[:len(folderRawStack)-1]
				folderTagStack = folderTagStack[:len(folderTagStack)-1]
			}
			return
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)
	return bookmarks, nil
}

func extractText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// normalizeTag converts a folder name to a lowercase tag.
func normalizeTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	// Remove characters that would be problematic in tags
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
