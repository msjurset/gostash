package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var importNotionCmd = &cobra.Command{
	Use:   "notion <path>",
	Short: "Import from Notion export (zip or directory)",
	Long: `Import pages and database rows from a Notion export.
Export from: Settings → Export all workspace content

Accepts a .zip file or an already-extracted directory.
Markdown files become snippets; CSV database rows become URL or snippet items.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportNotion,
}

func init() {
	importNotionCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importNotionCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importNotionCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importNotionCmd)
}

func runImportNotion(cmd *cobra.Command, args []string) error {
	path := args[0]
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	var items []importItem

	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat path: %w", err)
	}

	if fi.IsDir() {
		items, err = parseNotionDir(path)
	} else if strings.HasSuffix(path, ".zip") {
		items, err = parseNotionZip(path)
	} else {
		return fmt.Errorf("expected a .zip file or directory, got: %s", path)
	}
	if err != nil {
		return err
	}

	return importItems(cmd, items, extraTags, collection, dryRun)
}

type importItem struct {
	title     string
	url       string
	notes     string
	body      string
	tags      []string
	itemType  model.ItemType
	createdAt *time.Time
}

func importItems(cmd *cobra.Command, items []importItem, extraTags []string, collection string, dryRun bool) error {
	if dryRun {
		fmt.Printf("Found %d items. (dry run)\n\n", len(items))
		for _, it := range items {
			fmt.Printf("  [%s] %s\n", it.itemType.Display(), it.title)
			if it.url != "" {
				fmt.Printf("    url: %s\n", it.url)
			}
			if len(it.tags) > 0 {
				fmt.Printf("    tags: %s\n", strings.Join(it.tags, ", "))
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

	for _, it := range items {
		// Dedup URLs
		if it.url != "" {
			exists, err := s.ExistsByURL(ctx, it.url)
			if err != nil {
				return err
			}
			if exists {
				skipped++
				continue
			}
		}

		now := time.Now().UTC()
		entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
		id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

		created := now
		if it.createdAt != nil {
			created = *it.createdAt
		}

		item := &model.Item{
			ID:            id,
			Type:          it.itemType,
			Title:         it.title,
			URL:           it.url,
			Notes:         it.notes,
			ExtractedText: it.body,
			CreatedAt:     created,
			UpdatedAt:     now,
			Metadata:      json.RawMessage("{}"),
		}

		allTags := map[string]bool{}
		for _, t := range it.tags {
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
			fmt.Fprintf(os.Stderr, "warning: failed to import %q: %v\n", it.title, err)
			continue
		}
		imported++

		if !flagJSON {
			fmt.Printf("  Imported [%s] %s\n", shortID(id), it.title)
		}
	}

	if flagJSON {
		printJSON(map[string]int{"imported": imported, "skipped": skipped, "total": len(items)})
	} else {
		fmt.Printf("\nDone: %d imported, %d skipped, %d total\n", imported, skipped, len(items))
	}
	return nil
}

func parseNotionDir(dir string) ([]importItem, error) {
	var items []importItem
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(path, ".md"):
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			if it := parseMarkdownFile(f, filepath.Base(path)); it != nil {
				items = append(items, *it)
			}
		case strings.HasSuffix(path, ".csv"):
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			csvItems := parseCSVFile(f)
			items = append(items, csvItems...)
		}
		return nil
	})
	return items, err
}

func parseNotionZip(path string) ([]importItem, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	var items []importItem
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(f.Name, ".md"):
			rc, err := f.Open()
			if err != nil {
				continue
			}
			if it := parseMarkdownFile(rc, filepath.Base(f.Name)); it != nil {
				items = append(items, *it)
			}
			rc.Close()
		case strings.HasSuffix(f.Name, ".csv"):
			rc, err := f.Open()
			if err != nil {
				continue
			}
			csvItems := parseCSVFile(rc)
			items = append(items, csvItems...)
			rc.Close()
		}
	}
	return items, nil
}

func parseMarkdownFile(r io.Reader, filename string) *importItem {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) == 0 {
		return nil
	}

	title := strings.TrimSuffix(filename, ".md")
	// Clean Notion's hex-suffixed filenames (e.g., "My Page abc123def.md")
	if idx := strings.LastIndex(title, " "); idx > 0 {
		suffix := title[idx+1:]
		if len(suffix) >= 16 && isHex(suffix) {
			title = title[:idx]
		}
	}

	var tags []string
	var url string
	bodyStart := 0

	// Parse YAML frontmatter
	if lines[0] == "---" {
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				bodyStart = i + 1
				break
			}
			line := strings.TrimSpace(lines[i])
			if strings.HasPrefix(line, "tags:") {
				val := strings.TrimPrefix(line, "tags:")
				val = strings.TrimSpace(val)
				if val != "" {
					for _, t := range strings.Split(val, ",") {
						t = strings.TrimSpace(t)
						if t != "" {
							tags = append(tags, normalizeTag(t))
						}
					}
				}
			} else if strings.HasPrefix(line, "- ") && len(tags) > 0 {
				// YAML array continuation
				t := strings.TrimPrefix(line, "- ")
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, normalizeTag(t))
				}
			} else if strings.HasPrefix(line, "title:") {
				val := strings.TrimPrefix(line, "title:")
				val = strings.Trim(strings.TrimSpace(val), "\"'")
				if val != "" {
					title = val
				}
			} else if strings.HasPrefix(line, "url:") {
				val := strings.TrimPrefix(line, "url:")
				url = strings.TrimSpace(val)
			}
		}
	}

	body := strings.Join(lines[bodyStart:], "\n")
	body = strings.TrimSpace(body)

	itemType := model.TypeSnippet
	if url != "" {
		itemType = model.TypeURL
	}

	return &importItem{
		title:    title,
		url:      url,
		body:     body,
		tags:     tags,
		itemType: itemType,
	}
}

func parseCSVFile(r io.Reader) []importItem {
	cr := csv.NewReader(r)
	records, err := cr.ReadAll()
	if err != nil || len(records) < 2 {
		return nil
	}

	header := records[0]
	colIdx := map[string]int{}
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	getCol := func(row []string, names ...string) string {
		for _, name := range names {
			if idx, ok := colIdx[name]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
		}
		return ""
	}

	var items []importItem
	for _, row := range records[1:] {
		title := getCol(row, "name", "title")
		if title == "" {
			continue
		}

		url := getCol(row, "url", "link", "href")
		notes := getCol(row, "notes", "description", "summary")
		tagStr := getCol(row, "tags", "labels", "categories")

		var tags []string
		if tagStr != "" {
			for _, t := range strings.FieldsFunc(tagStr, func(r rune) bool { return r == ',' || r == ';' }) {
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, normalizeTag(t))
				}
			}
		}

		itemType := model.TypeSnippet
		if url != "" {
			itemType = model.TypeURL
		}

		items = append(items, importItem{
			title:    title,
			url:      url,
			notes:    notes,
			tags:     tags,
			itemType: itemType,
		})
	}
	return items
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
