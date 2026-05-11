package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var importRaindropCmd = &cobra.Command{
	Use:   "raindrop <file>",
	Short: "Import bookmarks from a Raindrop.io CSV export",
	Long: `Import bookmarks from a Raindrop.io CSV export.

Raindrop's export panel lives at:
  Settings → Backups → Export → CSV
Choose "All" (or any specific collection) — the export shape
is identical either way. Pass the downloaded .csv to this
command.

Each Raindrop bookmark becomes a URL item:
  - URL    = the bookmark URL.
  - Title  = the Raindrop title.
  - Tags   = Raindrop tags (already lowercase / hyphenated) +
             normalized folder-path segments, deduped.
  - Folder = the Raindrop collection path (Raindrop nests
             collections with "/" — preserved verbatim so the
             Mac importer's tree renders the original layout).
  - Notes  = Raindrop's "note" column, plus "excerpt" appended
             when both are present.

Duplicates by URL are skipped against existing stash items.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportRaindrop,
}

func init() {
	importRaindropCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importRaindropCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importRaindropCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importRaindropCmd)
}

func runImportRaindrop(cmd *cobra.Command, args []string) error {
	path := args[0]
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	bookmarks, err := parseRaindropCSV(path)
	if err != nil {
		return err
	}
	return runImportBookmarkList(bookmarks, extraTags, collection, dryRun, "Raindrop.io", path)
}

func parseRaindropCSV(path string) ([]bookmark, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx := indexCSVHeader(header)

	// Raindrop's column names are stable but we still match
	// loosely — protects against minor renames in future exports.
	urlCol := idx.find("url", "link")
	titleCol := idx.find("title", "name")
	noteCol := idx.find("note", "notes")
	excerptCol := idx.find("excerpt", "description")
	folderCol := idx.find("folder", "collection")
	tagsCol := idx.find("tags", "tag")
	createdCol := idx.find("created", "created at", "date")

	var out []bookmark
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		raw := strings.TrimSpace(getCol(row, urlCol))
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			continue
		}

		title := strings.TrimSpace(getCol(row, titleCol))
		if title == "" {
			title = raw
		}

		note := strings.TrimSpace(getCol(row, noteCol))
		excerpt := strings.TrimSpace(getCol(row, excerptCol))
		combinedNotes := note
		if excerpt != "" && excerpt != note {
			if combinedNotes != "" {
				combinedNotes = note + "\n\n" + excerpt
			} else {
				combinedNotes = excerpt
			}
		}

		bm := bookmark{
			url:   raw,
			title: title,
			notes: combinedNotes,
		}

		// Folder path — Raindrop nests collections with "/", so a
		// "Research/AI/Papers" value becomes a three-segment
		// breadcrumb. The Mac importer's tree picker walks it the
		// same way the Chrome breadcrumb does.
		if folder := strings.TrimSpace(getCol(row, folderCol)); folder != "" {
			var path []string
			for _, seg := range strings.Split(folder, "/") {
				seg = strings.TrimSpace(seg)
				if seg != "" {
					path = append(path, seg)
				}
			}
			bm.folderPath = path
			for _, f := range path {
				if tag := normalizeTag(f); tag != "" {
					bm.tags = append(bm.tags, tag)
				}
			}
		}

		// Raindrop's native tags column — comma-separated.
		if rawTags := strings.TrimSpace(getCol(row, tagsCol)); rawTags != "" {
			seen := make(map[string]bool)
			for _, t := range bm.tags {
				seen[t] = true
			}
			for _, t := range strings.Split(rawTags, ",") {
				t = strings.TrimSpace(t)
				if t == "" {
					continue
				}
				if n := normalizeTag(t); n != "" && !seen[n] {
					bm.tags = append(bm.tags, n)
					seen[n] = true
				}
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
