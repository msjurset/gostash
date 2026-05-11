package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// import history <browser> [path] — read recent visits from the
// browser's local history DB, dedup by URL, and import as stash
// items. Same dry-run/JSON dance as the bookmark importers so the
// Mac side can preview + curate before committing.
var importHistoryCmd = &cobra.Command{
	Use:   "history <browser> [path]",
	Short: "Import recent browser-history visits",
	Long: `Import recent visits from a browser's local history database.

Sources (history file format):
  - chrome / edge / brave / arc / vivaldi / opera / chromium
    Chromium-family SQLite ` + "`History`" + ` file.
  - firefox
    Firefox SQLite ` + "`places.sqlite`" + ` (same file as bookmarks).
  - safari
    Apple SQLite ` + "`History.db`" + ` (needs Full Disk Access).

Each visit becomes a candidate URL item carrying its title,
visit count, and last-visited timestamp. The CLI dedups by URL
against existing stash items so re-runs only surface what's new.

Use --since <days> to bound the window (default 15). Visits
older than the window are excluded server-side so the JSON
preview stays small even on multi-year histories.

Pass the optional path to override auto-discovery (useful when
running against a copied DB file rather than the live one).`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runImportHistory,
}

func init() {
	importHistoryCmd.Flags().IntP("since", "s", 15, "Look back this many days")
	importHistoryCmd.Flags().IntP("limit", "l", 5000, "Max visits to return")
	importHistoryCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importHistoryCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importHistoryCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importHistoryCmd)
}

func runImportHistory(cmd *cobra.Command, args []string) error {
	browser := strings.ToLower(args[0])
	var path string
	if len(args) > 1 {
		path = args[1]
	} else {
		discovered, err := historyPathForBrowser(browser)
		if err != nil {
			return fmt.Errorf("auto-discover %s history: %w", browser, err)
		}
		path = discovered
	}

	sinceDays, _ := cmd.Flags().GetInt("since")
	limit, _ := cmd.Flags().GetInt("limit")
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if sinceDays < 1 {
		sinceDays = 1
	}
	cutoff := time.Now().UTC().Add(-time.Duration(sinceDays) * 24 * time.Hour)

	bookmarks, err := readBrowserHistory(browser, path, cutoff, limit)
	if err != nil {
		// Safari-style FDA error gets the friendly hint, same as
		// the Safari bookmarks importer.
		if browser == "safari" && (errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "operation not permitted")) {
			return fmt.Errorf("cannot read Safari history (%s) — macOS blocks this without Full Disk Access. Grant via System Settings → Privacy & Security → Full Disk Access (add /Users/<you>/.local/bin/stash)", path)
		}
		return err
	}
	label := historyLabelForBrowser(browser)
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, label, path)
}

func historyLabelForBrowser(browser string) string {
	switch browser {
	case "chrome":   return "Chrome history"
	case "edge":     return "Edge history"
	case "brave":    return "Brave history"
	case "arc":      return "Arc history"
	case "vivaldi":  return "Vivaldi history"
	case "opera":    return "Opera history"
	case "chromium": return "Chromium history"
	case "firefox":  return "Firefox history"
	case "safari":   return "Safari history"
	default:         return strings.Title(browser) + " history"
	}
}

// historyPathForBrowser returns the default history-DB path for the
// named browser on macOS. Mirrors the bookmark-side discovery logic.
func historyPathForBrowser(browser string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch browser {
	case "chrome":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "Google", "Chrome"))
	case "edge":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "Microsoft Edge"))
	case "brave":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser"))
	case "arc":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "Arc", "User Data"))
	case "vivaldi":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "Vivaldi"))
	case "opera":
		// Opera doesn't have profile dirs — History sits at the root.
		return filepath.Join(home, "Library", "Application Support", "com.operasoftware.Opera", "History"), nil
	case "chromium":
		return chromiumHistoryPath(home, filepath.Join(home, "Library", "Application Support", "Chromium"))
	case "firefox":
		return firefoxPlacesPath()
	case "safari":
		return filepath.Join(home, "Library", "Safari", "History.db"), nil
	default:
		return "", fmt.Errorf("unknown browser %q (expected chrome / edge / brave / arc / vivaldi / opera / chromium / firefox / safari)", browser)
	}
}

// chromiumHistoryPath resolves the active-profile History file
// under a Chromium-family browser root. Reuses the same Local-State
// trick as `chromeBookmarksPath` but pointed at History instead of
// Bookmarks.
func chromiumHistoryPath(home, root string) (string, error) {
	profile := "Default"
	if data, err := os.ReadFile(filepath.Join(root, "Local State")); err == nil {
		var ls struct {
			Profile struct {
				LastUsed    string   `json:"last_used"`
				LastActives []string `json:"last_active_profiles"`
			} `json:"profile"`
		}
		if json.Unmarshal(data, &ls) == nil {
			if ls.Profile.LastUsed != "" {
				profile = ls.Profile.LastUsed
			} else if len(ls.Profile.LastActives) > 0 {
				profile = ls.Profile.LastActives[0]
			}
		}
	}
	candidate := filepath.Join(root, profile, "History")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	// Fall back to Default if the named profile doesn't have a
	// History file yet.
	fallback := filepath.Join(root, "Default", "History")
	return fallback, nil
}

// readBrowserHistory dispatches to the per-browser SQL query.
// Returns history items as []bookmark so they flow through the
// shared runImportBookmarkList path unchanged.
func readBrowserHistory(browser, path string, cutoff time.Time, limit int) ([]bookmark, error) {
	switch browser {
	case "chrome", "edge", "brave", "arc", "vivaldi", "opera", "chromium":
		return readChromiumHistory(path, cutoff, limit)
	case "firefox":
		return readFirefoxHistory(path, cutoff, limit)
	case "safari":
		return readSafariHistory(path, cutoff, limit)
	default:
		return nil, fmt.Errorf("unknown browser %q", browser)
	}
}

// Chromium History DB:
//   urls: id, url, title, visit_count, typed_count, last_visit_time, hidden
// `last_visit_time` is microseconds since the Windows epoch
// (1601-01-01 UTC) — same as Chrome's bookmark date_added.
func readChromiumHistory(path string, cutoff time.Time, limit int) ([]bookmark, error) {
	dsn := "file:" + path + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open chromium history: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping chromium history: %w", err)
	}
	const winEpochToUnixSec = 11_644_473_600
	cutoffMicros := (cutoff.Unix() + winEpochToUnixSec) * 1_000_000
	rows, err := db.Query(`
        SELECT url, IFNULL(title, ''), visit_count, last_visit_time
        FROM urls
        WHERE hidden = 0
          AND last_visit_time > ?
        ORDER BY last_visit_time DESC
        LIMIT ?`,
		cutoffMicros, limit)
	if err != nil {
		return nil, fmt.Errorf("query chromium history: %w", err)
	}
	defer rows.Close()
	var out []bookmark
	for rows.Next() {
		var url, title string
		var visitCount int
		var lastVisitMicros int64
		if err := rows.Scan(&url, &title, &visitCount, &lastVisitMicros); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			continue
		}
		if title == "" {
			title = url
		}
		unixMicros := lastVisitMicros - (winEpochToUnixSec * 1_000_000)
		t := time.Unix(unixMicros/1_000_000, (unixMicros%1_000_000)*1_000).UTC()
		out = append(out, bookmark{
			url:       url,
			title:     title,
			createdAt: &t,
		})
	}
	return out, rows.Err()
}

// Firefox places.sqlite:
//   moz_places: id, url, title, visit_count, last_visit_date
// `last_visit_date` is microseconds since the Unix epoch.
func readFirefoxHistory(path string, cutoff time.Time, limit int) ([]bookmark, error) {
	dsn := "file:" + path + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open firefox history: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping firefox history: %w", err)
	}
	cutoffMicros := cutoff.UnixMicro()
	rows, err := db.Query(`
        SELECT url, IFNULL(title, ''), IFNULL(visit_count, 0), last_visit_date
        FROM moz_places
        WHERE last_visit_date IS NOT NULL
          AND last_visit_date > ?
        ORDER BY last_visit_date DESC
        LIMIT ?`,
		cutoffMicros, limit)
	if err != nil {
		return nil, fmt.Errorf("query firefox history: %w", err)
	}
	defer rows.Close()
	var out []bookmark
	for rows.Next() {
		var url, title string
		var visitCount int
		var lastVisitMicros int64
		if err := rows.Scan(&url, &title, &visitCount, &lastVisitMicros); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			continue
		}
		if title == "" {
			title = url
		}
		t := time.UnixMicro(lastVisitMicros).UTC()
		out = append(out, bookmark{
			url:       url,
			title:     title,
			createdAt: &t,
		})
	}
	return out, rows.Err()
}

// Safari History.db:
//   history_items: id, url, visit_count
//   history_visits: history_item (FK), visit_time, title
// `visit_time` is CFAbsoluteTime — seconds since 2001-01-01 UTC.
func readSafariHistory(path string, cutoff time.Time, limit int) ([]bookmark, error) {
	dsn := "file:" + path + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open safari history: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping safari history: %w", err)
	}
	// CFAbsoluteTime base: 2001-01-01 UTC; Unix base: 1970-01-01.
	const cfAbsoluteOffset = 978_307_200
	cutoffCF := float64(cutoff.Unix() - cfAbsoluteOffset)
	// Aggregate the latest visit per URL — history_visits has one
	// row per visit, history_items one row per unique URL.
	rows, err := db.Query(`
        SELECT items.url,
               IFNULL((SELECT title FROM history_visits
                       WHERE history_item = items.id AND title IS NOT NULL
                       ORDER BY visit_time DESC LIMIT 1), '') AS title,
               items.visit_count,
               (SELECT MAX(visit_time) FROM history_visits WHERE history_item = items.id) AS last_visit
        FROM history_items items
        WHERE last_visit > ?
        ORDER BY last_visit DESC
        LIMIT ?`,
		cutoffCF, limit)
	if err != nil {
		return nil, fmt.Errorf("query safari history: %w", err)
	}
	defer rows.Close()
	var out []bookmark
	for rows.Next() {
		var url, title string
		var visitCount int
		var lastVisitCF float64
		if err := rows.Scan(&url, &title, &visitCount, &lastVisitCF); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			continue
		}
		if title == "" {
			title = url
		}
		unixSec := int64(lastVisitCF) + cfAbsoluteOffset
		t := time.Unix(unixSec, 0).UTC()
		out = append(out, bookmark{
			url:       url,
			title:     title,
			createdAt: &t,
		})
	}
	return out, rows.Err()
}
