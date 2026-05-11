package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"howett.net/plist"
)

var importSafariCmd = &cobra.Command{
	Use:   "safari [path]",
	Short: "Import bookmarks from Safari's Bookmarks.plist",
	Long: `Import directly from Safari's bookmark store (no manual export
required) — provided the binary has Full Disk Access. Without
FDA, macOS's TCC layer blocks the read and the import will fail
with "operation not permitted"; in that case grant access via:

  System Settings → Privacy & Security → Full Disk Access → +
  → /Users/<you>/.local/bin/stash

Or, as a friction-free alternative: in Safari, File → Export
Bookmarks…, then use 'stash import bookmarks' on the HTML.

Folder names become tags (lowercased, hyphens-for-spaces).
Reading-list-only entries (Apple's "Reading List" group) are
skipped — they're not user-curated bookmarks in the usual sense.
Duplicates by URL are skipped.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImportSafari,
}

func init() {
	importSafariCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importSafariCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importSafariCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importSafariCmd)
}

func runImportSafari(cmd *cobra.Command, args []string) error {
	var path string
	if len(args) > 0 {
		path = args[0]
	} else {
		discovered, err := safariBookmarksPath()
		if err != nil {
			return fmt.Errorf("auto-discover Safari bookmarks: %w", err)
		}
		path = discovered
	}

	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	bookmarks, err := parseSafariBookmarks(path)
	if err != nil {
		// Re-wrap TCC errors with the FDA pointer — the bare
		// "operation not permitted" doesn't tell a typical user
		// what's actually going on.
		if errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "operation not permitted") {
			return fmt.Errorf("cannot read Safari bookmarks (%s) — macOS blocks this without Full Disk Access. Grant via System Settings → Privacy & Security → Full Disk Access (add /Users/<you>/.local/bin/stash), or export bookmarks via Safari → File → Export Bookmarks and use 'stash import bookmarks' on the HTML", path)
		}
		return err
	}
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, "Safari", path)
}

// safariBookmarksPath returns the canonical Safari bookmarks file
// location on macOS. Single location — Safari doesn't have profiles
// like Chrome / Firefox.
func safariBookmarksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Safari", "Bookmarks.plist"), nil
}

// safariNode mirrors the on-disk structure of a single entry in
// Safari's bookmarks plist. The schema has been stable for many
// macOS releases.
type safariNode struct {
	WebBookmarkType string         `plist:"WebBookmarkType"`
	Title           string         `plist:"Title"`
	URIDictionary   map[string]any `plist:"URIDictionary"`
	URLString       string         `plist:"URLString"`
	Children        []safariNode   `plist:"Children"`
}

// parseSafariBookmarks reads Safari's Bookmarks.plist and returns a
// flattened bookmark list, with each entry's tags reflecting the
// folder path it sat under.
func parseSafariBookmarks(path string) ([]bookmark, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	dec := plist.NewDecoder(f)
	var root safariNode
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse Safari plist: %w", err)
	}

	var out []bookmark
	walkSafariNode(&out, root, nil)
	return out, nil
}

func walkSafariNode(out *[]bookmark, node safariNode, folderPath []string) {
	switch node.WebBookmarkType {
	case "WebBookmarkTypeLeaf":
		url := node.URLString
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return
		}
		title := ""
		if t, ok := node.URIDictionary["title"].(string); ok {
			title = t
		}
		if title == "" {
			title = url
		}
		bm := bookmark{
			url:        url,
			title:      title,
			folderPath: append([]string{}, folderPath...),
		}
		for _, f := range folderPath {
			if tag := normalizeTag(f); tag != "" {
				bm.tags = append(bm.tags, tag)
			}
		}
		*out = append(*out, bm)
	case "WebBookmarkTypeList":
		// Skip Apple's internal Reading List group — those are
		// "save for later" entries, not user-curated bookmarks.
		// Their plist name is "com.apple.ReadingList".
		if node.Title == "com.apple.ReadingList" {
			return
		}
		nextPath := folderPath
		// Replace BookmarksBar / BookmarksMenu with friendlier
		// names so the resulting tags read like the user expects.
		// Drop the root labels entirely (similar to Chrome's
		// internal "bookmark_bar" / "other" labels).
		labeled := node.Title
		switch labeled {
		case "BookmarksBar":
			labeled = "bookmarks-bar"
		case "BookmarksMenu":
			labeled = "bookmarks-menu"
		case "":
			labeled = ""
		}
		if labeled != "" {
			nextPath = append(append([]string{}, folderPath...), labeled)
		}
		for _, child := range node.Children {
			walkSafariNode(out, child, nextPath)
		}
	}
}
