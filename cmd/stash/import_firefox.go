package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

var importFirefoxCmd = &cobra.Command{
	Use:   "firefox [path]",
	Short: "Import bookmarks from Firefox's places.sqlite",
	Long: `Import directly from Firefox's native places.sqlite (no manual
HTML export required).

When run without arguments, looks at the system's default Firefox
profile path:

  ~/Library/Application Support/Firefox/Profiles/<default>/places.sqlite

The default profile is read from profiles.ini. Pass an explicit
path to import from a different profile or a copied database.
Folder names become tags; Firefox's native tag system is preserved
(tags are stored under a special "Tags" root that we walk
separately). Duplicates by URL are skipped.

The places.sqlite file is opened read-only with WAL-aware locking,
so Firefox can be running while this command executes.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImportFirefox,
}

func init() {
	importFirefoxCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importFirefoxCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importFirefoxCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importFirefoxCmd)
}

func runImportFirefox(cmd *cobra.Command, args []string) error {
	var path string
	if len(args) > 0 {
		path = args[0]
	} else {
		discovered, err := firefoxPlacesPath()
		if err != nil {
			return fmt.Errorf("auto-discover Firefox bookmarks: %w", err)
		}
		path = discovered
	}

	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	bookmarks, err := parseFirefoxBookmarks(path)
	if err != nil {
		return err
	}
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, "Firefox", path)
}

// firefoxPlacesPath returns the places.sqlite file for the user's
// default Firefox profile on macOS. Reads profiles.ini to find which
// profile is "Default=1". Falls back to the first *.default* directory
// in Profiles/ if profiles.ini doesn't name one.
func firefoxPlacesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "Library", "Application Support", "Firefox")

	if profile, ok := firefoxDefaultFromIni(filepath.Join(root, "profiles.ini")); ok {
		// profiles.ini's Path is relative to Firefox/, e.g. `Profiles/abc123.default`.
		candidate := filepath.Join(root, profile, "places.sqlite")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Fallback: the lone Profiles/<*.default*>/places.sqlite.
	entries, _ := os.ReadDir(filepath.Join(root, "Profiles"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Firefox profile dirs are typically named <random>.default,
		// <random>.default-release, etc.
		if strings.Contains(e.Name(), ".default") {
			candidate := filepath.Join(root, "Profiles", e.Name(), "places.sqlite")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("no Firefox profile found under %s", root)
}

// firefoxDefaultFromIni parses profiles.ini and returns the Path of
// the section that has Default=1. The .ini format is simple enough
// that bringing in an INI parser would be overkill — line-oriented
// scan. Returns ("", false) when no default is named.
func firefoxDefaultFromIni(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")

	type section struct {
		path      string
		isDefault bool
		isProfile bool
	}
	var sections []section
	cur := -1
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections = append(sections, section{
				isProfile: strings.HasPrefix(line, "[Profile"),
			})
			cur = len(sections) - 1
			continue
		}
		if cur < 0 {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			switch strings.TrimSpace(k) {
			case "Path":
				sections[cur].path = strings.TrimSpace(v)
			case "Default":
				if strings.TrimSpace(v) == "1" {
					sections[cur].isDefault = true
				}
			}
		}
	}
	for _, s := range sections {
		if s.isProfile && s.isDefault && s.path != "" {
			return s.path, true
		}
	}
	return "", false
}

// parseFirefoxBookmarks opens places.sqlite read-only and walks
// the bookmark tree. Folders become tag breadcrumbs; Firefox's
// native tag system (tags-as-folders under the Tags root, id=4)
// also contributes tags to URLs that appear under those folders.
func parseFirefoxBookmarks(path string) ([]bookmark, error) {
	// `?mode=ro` opens read-only; `&_journal_mode=OFF` skips the
	// journal so we don't accidentally write next to a running
	// Firefox. modernc/sqlite honors URI parameters via DSN.
	dsn := "file:" + path + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open places.sqlite: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping places.sqlite: %w", err)
	}

	// Firefox's bookmark tree:
	// - moz_bookmarks: id, parent, type (1=url, 2=folder, 3=separator),
	//                  fk (-> moz_places.id), title, dateAdded
	// - moz_places: id, url
	// Root ids: 1 = root, 2 = bookmark menu, 3 = toolbar,
	//           4 = tags, 5 = unfiled, 6 = mobile.
	// Tag folders live under id=4 — each folder is a tag, and
	// bookmarks under those folders mark their fk URL with that tag.

	// First pass: load all folders to build a path map.
	folders := map[int64]folder{}
	rows, err := db.Query(`SELECT id, IFNULL(parent, 0), IFNULL(title, '') FROM moz_bookmarks WHERE type = 2`)
	if err != nil {
		return nil, fmt.Errorf("query folders: %w", err)
	}
	for rows.Next() {
		var f folder
		if err := rows.Scan(&f.id, &f.parent, &f.title); err != nil {
			rows.Close()
			return nil, err
		}
		folders[f.id] = f
	}
	rows.Close()

	// Second pass: tag relations. URLs under id=4's descendants
	// pick up the immediate folder name as a tag.
	tagsByPlace := map[int64][]string{}
	rows, err = db.Query(`
		SELECT b.fk, p.title
		FROM moz_bookmarks b
		JOIN moz_bookmarks p ON p.id = b.parent
		WHERE b.type = 1 AND b.fk IS NOT NULL AND p.parent = 4
	`)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	for rows.Next() {
		var fk int64
		var tag string
		if err := rows.Scan(&fk, &tag); err != nil {
			rows.Close()
			return nil, err
		}
		clean := normalizeTag(tag)
		if clean != "" {
			tagsByPlace[fk] = append(tagsByPlace[fk], clean)
		}
	}
	rows.Close()

	// Third pass: actual bookmark URLs (excluding entries under
	// the Tags root, which are tag-association rows, not real
	// bookmarks).
	rows, err = db.Query(`
		SELECT b.fk, b.parent, IFNULL(b.title, ''), IFNULL(b.dateAdded, 0), p.url
		FROM moz_bookmarks b
		JOIN moz_places p ON p.id = b.fk
		WHERE b.type = 1
		  AND p.url LIKE 'http%'
	`)
	if err != nil {
		return nil, fmt.Errorf("query bookmarks: %w", err)
	}
	defer rows.Close()

	var out []bookmark
	for rows.Next() {
		var fk, parent, dateAdded int64
		var title, url string
		if err := rows.Scan(&fk, &parent, &title, &dateAdded, &url); err != nil {
			return nil, err
		}
		// Skip rows whose parent is a tag folder — those are
		// tag-associations, not standalone bookmarks. We harvested
		// their tag info above.
		if isUnderTagsRoot(parent, folders) {
			continue
		}
		bm := bookmark{url: url, title: title}
		if bm.title == "" {
			bm.title = url
		}
		// Folder breadcrumbs — walk parent chain, keep the raw
		// names for the Mac importer's tree UI and add normalized
		// versions to the tag list.
		rawPath := folderPathFor(parent, folders)
		bm.folderPath = append([]string{}, rawPath...)
		for _, name := range rawPath {
			if t := normalizeTag(name); t != "" {
				bm.tags = append(bm.tags, t)
			}
		}
		// Native Firefox tags (separate from folder breadcrumbs).
		bm.tags = append(bm.tags, tagsByPlace[fk]...)
		// Firefox dateAdded is microseconds since the Unix epoch
		// (not Windows). Conversion is a simple divide.
		if dateAdded > 0 {
			t := time.UnixMicro(dateAdded).UTC()
			bm.createdAt = &t
		}
		out = append(out, bm)
	}
	return out, nil
}

// folderPathFor walks the parent chain for a bookmark's parent
// folder id, returning folder names from outermost to innermost.
// Stops at root or when parent points outside the folder map.
// Skips the well-known root labels (Bookmarks Menu, Toolbar, etc.)
// since users don't think of those as tags.
func folderPathFor(parentID int64, folders map[int64]folder) []string {
	var path []string
	cur := parentID
	for cur != 0 {
		f, ok := folders[cur]
		if !ok {
			break
		}
		// Skip Firefox's internal root-level folders.
		if !isRootFolder(f.id) && f.title != "" {
			path = append([]string{f.title}, path...)
		}
		cur = f.parent
	}
	return path
}

// isUnderTagsRoot returns true if the given folder id sits beneath
// the Tags root (id=4) — used to exclude tag-association bookmarks
// from the regular bookmark walk.
func isUnderTagsRoot(folderID int64, folders map[int64]folder) bool {
	cur := folderID
	for cur != 0 {
		if cur == 4 {
			return true
		}
		f, ok := folders[cur]
		if !ok {
			return false
		}
		cur = f.parent
	}
	return false
}

func isRootFolder(id int64) bool {
	switch id {
	case 1, 2, 3, 4, 5, 6:
		return true
	}
	return false
}

// folder is duplicated locally so this file doesn't need to import
// the type from elsewhere.
type folder struct {
	id, parent int64
	title      string
}
