package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var importChromeCmd = &cobra.Command{
	Use:   "chrome [path]",
	Short: "Import bookmarks from Chrome's live JSON file",
	Long: `Import directly from Chrome's native Bookmarks file (no manual
HTML export required).

When run without arguments, looks at the system's default Chrome
profile path:

  ~/Library/Application Support/Google/Chrome/<active-profile>/Bookmarks

The active profile is read from Chrome's Local State file
(profile.last_used). Pass an explicit path to import from a
different profile or a copied bookmark file. Folder names become
tags; duplicates by URL are skipped.

Chrome can be running while this command executes — the Bookmarks
file is JSON, not SQLite, and is rewritten atomically on save.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImportChrome,
}

func init() {
	importChromeCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importChromeCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importChromeCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importChromeCmd)
}

func runImportChrome(cmd *cobra.Command, args []string) error {
	var path string
	if len(args) > 0 {
		path = args[0]
	} else {
		discovered, err := chromeBookmarksPath()
		if err != nil {
			return fmt.Errorf("auto-discover Chrome bookmarks: %w", err)
		}
		path = discovered
	}

	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	bookmarks, err := parseChromeBookmarks(path)
	if err != nil {
		return err
	}
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, "Chrome", path)
}

// chromeBookmarksPath returns the Bookmarks file for the user's
// most-recently-used Chrome profile on macOS. Falls back to the
// "Default" profile when Local State is missing or doesn't name a
// last-used profile.
func chromeBookmarksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")

	// Read Local State to find the active profile.
	profile := "Default"
	if data, err := os.ReadFile(filepath.Join(root, "Local State")); err == nil {
		var ls struct {
			Profile struct {
				LastUsed    string   `json:"last_used"`
				LastActives []string `json:"last_active_profiles"`
			} `json:"profile"`
		}
		if err := json.Unmarshal(data, &ls); err == nil {
			if ls.Profile.LastUsed != "" {
				profile = ls.Profile.LastUsed
			} else if len(ls.Profile.LastActives) > 0 {
				profile = ls.Profile.LastActives[0]
			}
		}
	}

	candidate := filepath.Join(root, profile, "Bookmarks")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	// Fall back to Default if the named profile doesn't actually
	// have a Bookmarks file (rare — possible right after a clean
	// install before the user has saved any bookmarks).
	return filepath.Join(root, "Default", "Bookmarks"), nil
}

// chromeBookmarkNode is the on-disk structure of a single entry in
// Chrome's Bookmarks file. Folders carry `children`; URL entries
// carry `url` + `date_added`.
type chromeBookmarkNode struct {
	Type      string               `json:"type"` // "url" | "folder"
	Name      string               `json:"name"`
	URL       string               `json:"url,omitempty"`
	DateAdded string               `json:"date_added,omitempty"`
	Children  []chromeBookmarkNode `json:"children,omitempty"`
}

// parseChromeBookmarks reads Chrome's Bookmarks JSON and returns a
// flattened bookmark list, with each entry's tags reflecting the
// folder path it sat under.
func parseChromeBookmarks(path string) ([]bookmark, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var doc struct {
		Roots map[string]chromeBookmarkNode `json:"roots"`
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse Chrome JSON: %w", err)
	}

	var out []bookmark
	for rootKey, root := range doc.Roots {
		// Skip the implicit "checksum" / "version" sibling fields
		// that share the JSON object — they're not real subtrees.
		if root.Type == "" && len(root.Children) == 0 {
			continue
		}
		// Don't tag with the root folder name itself ("bookmark_bar"
		// / "other" / "synced") — those are Chrome-internal labels
		// the user never sees. Folders below the root get tagged.
		_ = rootKey
		walkChromeNode(&out, root, nil)
	}
	return out, nil
}

func walkChromeNode(out *[]bookmark, node chromeBookmarkNode, folderPath []string) {
	switch node.Type {
	case "url":
		if !strings.HasPrefix(node.URL, "http://") && !strings.HasPrefix(node.URL, "https://") {
			return
		}
		bm := bookmark{
			url:   node.URL,
			title: node.Name,
		}
		if bm.title == "" {
			bm.title = node.URL
		}
		// Preserve the raw folder breadcrumb so the Mac importer's
		// tree UI can render the original hierarchy.
		bm.folderPath = append([]string{}, folderPath...)
		for _, f := range folderPath {
			tag := normalizeTag(f)
			if tag != "" {
				bm.tags = append(bm.tags, tag)
			}
		}
		if t := chromeDateAddedToTime(node.DateAdded); t != nil {
			bm.createdAt = t
		}
		*out = append(*out, bm)
	case "folder":
		nextPath := folderPath
		if node.Name != "" {
			nextPath = append(append([]string{}, folderPath...), node.Name)
		}
		for _, child := range node.Children {
			walkChromeNode(out, child, nextPath)
		}
	}
}

// chromeDateAddedToTime decodes Chrome's `date_added` value, which is
// the number of microseconds since the Windows epoch (Jan 1, 1601 UTC).
// Empty / zero / unparseable returns nil so the caller falls back to
// the import time.
func chromeDateAddedToTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	micros, err := strconv.ParseInt(s, 10, 64)
	if err != nil || micros == 0 {
		return nil
	}
	// Microseconds between 1601-01-01 and 1970-01-01.
	const winEpochToUnixSec = 11_644_473_600
	unixMicros := micros - (winEpochToUnixSec * 1_000_000)
	t := time.Unix(unixMicros/1_000_000, (unixMicros%1_000_000)*1_000).UTC()
	return &t
}

// runImportBookmarkList is the shared back-end for all bookmark
// importers (Netscape HTML, Pocket, Chrome JSON, Firefox SQLite).
// Takes the already-parsed bookmark list plus the per-command flags
// and writes the items out, deduping by URL.
func runImportBookmarkList(bookmarks []bookmark, extraTags []string, collection string, dryRun bool, sourceLabel, sourcePath string) error {
	if dryRun {
		if flagJSON {
			// Structured preview for the Mac importer's tree UI.
			// Each bookmark carries its folder breadcrumb (raw
			// display names) plus its default tags (normalized
			// folder-derived + caller-supplied extras). The Mac
			// app builds the tree from `folder_path` and lets the
			// user edit tags before committing via `import apply`.
			type bookmarkJSON struct {
				URL            string   `json:"url"`
				Title          string   `json:"title"`
				FolderPath     []string `json:"folder_path"`
				DefaultTags    []string `json:"default_tags"`
				CreatedAt      *string  `json:"created_at,omitempty"`
				Notes          string   `json:"notes,omitempty"`
				AlreadyInStash bool     `json:"already_in_stash"`
			}
			// Open the store read-only so the dry-run can flag
			// URLs that would be skipped on commit. The Mac
			// importer uses this to default-uncheck duplicates and
			// prepend "DUPLICATE" to their titles. Failure to open
			// (e.g. missing DB) just leaves the flag false — dedup
			// still happens server-side when the user clicks
			// Import, so the worst case is the user sees an
			// already-stashed item as importable and the apply
			// step silently skips it.
			s, err := openStore()
			var existsByURL map[string]bool
			if err == nil {
				defer s.Close()
				ctx := context.Background()
				existsByURL = make(map[string]bool, len(bookmarks))
				for _, bm := range bookmarks {
					if exists, e := s.ExistsByURL(ctx, bm.url); e == nil {
						existsByURL[bm.url] = exists
					}
				}
			}
			out := make([]bookmarkJSON, 0, len(bookmarks))
			for _, bm := range bookmarks {
				tagSet := make(map[string]bool)
				for _, t := range bm.tags {
					tagSet[t] = true
				}
				for _, t := range extraTags {
					tagSet[t] = true
				}
				tags := make([]string, 0, len(tagSet))
				for t := range tagSet {
					tags = append(tags, t)
				}
				j := bookmarkJSON{
					URL:            bm.url,
					Title:          bm.title,
					FolderPath:     bm.folderPath,
					DefaultTags:    tags,
					Notes:          bm.notes,
					AlreadyInStash: existsByURL[bm.url],
				}
				if bm.createdAt != nil {
					ts := bm.createdAt.Format(time.RFC3339)
					j.CreatedAt = &ts
				}
				out = append(out, j)
			}
			printJSON(map[string]any{
				"source":    sourceLabel,
				"path":      sourcePath,
				"bookmarks": out,
			})
			return nil
		}
		fmt.Printf("Found %d bookmarks in %s (%s). (dry run — nothing will be saved)\n\n",
			len(bookmarks), sourceLabel, sourcePath)
		for _, bm := range bookmarks {
			tags := append(append([]string{}, bm.tags...), extraTags...)
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

		all := make(map[string]bool)
		for _, t := range bm.tags {
			all[t] = true
		}
		for _, t := range extraTags {
			all[t] = true
		}
		for t := range all {
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
		printJSON(map[string]any{
			"source":   sourceLabel,
			"path":     sourcePath,
			"imported": imported,
			"skipped":  skipped,
			"total":    len(bookmarks),
		})
	} else {
		fmt.Printf("\n%s: %d imported, %d skipped (duplicate), %d total\n",
			sourceLabel, imported, skipped, len(bookmarks))
	}
	return nil
}
