package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var importPinterestCmd = &cobra.Command{
	Use:   "pinterest <file>",
	Short: "Import pins from a Pinterest data-download CSV",
	Long: `Import bookmarks from Pinterest's data download.

Pinterest exposes a "Request a copy of your data" flow at:
  Settings → Privacy and data → Request a copy of your data
After the download arrives, extract the .zip and pass the
'pins.csv' (or 'pins-all.csv', exact name varies by export
version) to this command.

Each pin becomes a URL item:
  - URL    = source URL (the external page the pin links to);
             falls back to the pin's image URL when the source
             column is empty (image-only pins).
  - Title  = pin title; falls back to the first line of the
             description.
  - Tags   = ["pinterest", normalized-board-name].
  - Folder = board name — surfaces in the Mac importer's tree
             so you can pick all-from-one-board.
  - Notes  = pin description.

Pinterest's export schema has changed several times; this
parser matches columns by name (case-insensitive) so older /
newer exports both work. Duplicates by URL are skipped.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportPinterest,
}

func init() {
	importPinterestCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importPinterestCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importPinterestCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importPinterestCmd)
}

func runImportPinterest(cmd *cobra.Command, args []string) error {
	path := args[0]
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	bookmarks, err := parsePinterestCSV(path)
	if err != nil {
		return err
	}
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, "Pinterest", path)
}

func parsePinterestCSV(path string) ([]bookmark, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx := indexCSVHeader(header)

	// Column lookup — Pinterest's export has shifted column names
	// across versions, so each lookup tries a few aliases.
	urlCol := idx.find("source url", "source", "pin url", "url")
	imgCol := idx.find("image url", "image", "cover")
	titleCol := idx.find("title", "pin title", "name")
	descCol := idx.find("description", "note", "pin description")
	boardCol := idx.find("board name", "board", "board title")
	createdCol := idx.find("created at", "created", "date", "saved at")

	var out []bookmark
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		// Prefer source URL; fall back to image URL when the pin
		// is an image-only save with no external link.
		raw := getCol(row, urlCol)
		if raw == "" {
			raw = getCol(row, imgCol)
		}
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			continue
		}

		title := strings.TrimSpace(getCol(row, titleCol))
		desc := strings.TrimSpace(getCol(row, descCol))
		if title == "" {
			// Use first line of description as a title fallback —
			// Pinterest pins are often title-less but description-rich.
			if firstLine, _, ok := strings.Cut(desc, "\n"); ok {
				title = firstLine
			} else {
				title = desc
			}
		}
		if title == "" {
			title = raw
		}

		bm := bookmark{
			url:   raw,
			title: title,
			notes: desc,
			tags:  []string{"pinterest"},
		}
		board := strings.TrimSpace(getCol(row, boardCol))
		if board != "" {
			bm.folderPath = []string{board}
			if tag := normalizeTag(board); tag != "" {
				bm.tags = append(bm.tags, tag)
			}
		}
		if ts := strings.TrimSpace(getCol(row, createdCol)); ts != "" {
			if t := parseFlexibleTime(ts); t != nil {
				bm.createdAt = t
			}
		}
		out = append(out, bm)
	}
	return out, nil
}

// csvHeaderIndex maps lowercased header labels to their column
// position. `find` returns the first matching column for any of
// the given aliases, or -1 when none match.
type csvHeaderIndex map[string]int

func indexCSVHeader(header []string) csvHeaderIndex {
	m := make(csvHeaderIndex, len(header))
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		// Strip a UTF-8 BOM (U+FEFF) from the first column header.
		// Excel and some web exporters prepend one, and the BOM would
		// silently break case-insensitive lookups otherwise.
		key = strings.TrimPrefix(key, "\ufeff")
		if _, exists := m[key]; !exists {
			m[key] = i
		}
	}
	return m
}

func (m csvHeaderIndex) find(aliases ...string) int {
	for _, a := range aliases {
		if i, ok := m[a]; ok {
			return i
		}
	}
	return -1
}

func getCol(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

// parseFlexibleTime tries a handful of common timestamp formats.
// Returns nil when none parse. Used by CSV-format importers
// where the date column shape isn't guaranteed.
func parseFlexibleTime(s string) *time.Time {
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
		"01/02/2006",
		"2006/01/02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
