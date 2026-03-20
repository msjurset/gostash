package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"time"

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

func init() {
	importBookmarksCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported bookmarks")
	importBookmarksCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importBookmarksCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importBookmarksCmd)
	rootCmd.AddCommand(importCmd)
}

type bookmark struct {
	url   string
	title string
	tags  []string // derived from folder path
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

		item := &model.Item{
			ID:        id,
			Type:      model.TypeURL,
			Title:     bm.title,
			URL:       bm.url,
			CreatedAt: now,
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
	var folderStack []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "h3":
				// Folder heading — push folder name onto stack
				if text := extractText(n); text != "" {
					folderStack = append(folderStack, normalizeTag(text))
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
					for _, t := range folderStack {
						if t != "" {
							tags = append(tags, t)
						}
					}
					bookmarks = append(bookmarks, bookmark{
						url:   href,
						title: title,
						tags:  tags,
					})
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
			if len(folderStack) > 0 {
				folderStack = folderStack[:len(folderStack)-1]
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
