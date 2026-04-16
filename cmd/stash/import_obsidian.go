package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var importObsidianCmd = &cobra.Command{
	Use:   "obsidian <vault-path>",
	Short: "Import notes from an Obsidian vault",
	Long: `Import markdown notes from an Obsidian vault directory.
The .obsidian/ config directory is automatically skipped.

YAML frontmatter tags are preserved. [[Wikilinks]] between notes are
resolved into item links after import.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportObsidian,
}

func init() {
	importObsidianCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importObsidianCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importObsidianCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importObsidianCmd)
}

var wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)

func runImportObsidian(cmd *cobra.Command, args []string) error {
	vaultPath := args[0]
	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	fi, err := os.Stat(vaultPath)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", vaultPath)
	}

	type obsNote struct {
		importItem
		wikilinks []string
		filePath  string
	}

	var notes []obsNote

	err = filepath.Walk(vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip .obsidian config and hidden dirs
		if info.IsDir() && (strings.HasPrefix(info.Name(), ".")) {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		note := parseObsidianNote(f, path, vaultPath)
		notes = append(notes, note)
		return nil
	})
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("Found %d notes. (dry run)\n\n", len(notes))
		for _, n := range notes {
			fmt.Printf("  %s\n", n.title)
			if len(n.tags) > 0 {
				fmt.Printf("    tags: %s\n", strings.Join(n.tags, ", "))
			}
			if len(n.wikilinks) > 0 {
				fmt.Printf("    links: %s\n", strings.Join(n.wikilinks, ", "))
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
	// Import all notes, track title→ID for wikilink resolution
	titleToID := map[string]string{}
	var imported int

	for _, n := range notes {
		now := time.Now().UTC()
		entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
		id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

		created := now
		if n.createdAt != nil {
			created = *n.createdAt
		}

		item := &model.Item{
			ID:            id,
			Type:          model.TypeSnippet,
			Title:         n.title,
			Notes:         n.notes,
			ExtractedText: n.body,
			SourcePath:    n.filePath,
			MimeType:      "text/markdown",
			CreatedAt:     created,
			UpdatedAt:     now,
			Metadata:      json.RawMessage("{}"),
		}

		allTags := map[string]bool{}
		for _, t := range n.tags {
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
			fmt.Fprintf(os.Stderr, "warning: failed to import %q: %v\n", n.title, err)
			continue
		}

		titleToID[strings.ToLower(n.title)] = id
		imported++

		if !flagJSON {
			fmt.Printf("  Imported [%s] %s\n", shortID(id), n.title)
		}
	}

	// Resolve wikilinks into item links
	var linked int
	for _, n := range notes {
		fromID, ok := titleToID[strings.ToLower(n.title)]
		if !ok {
			continue
		}
		for _, target := range n.wikilinks {
			toID, ok := titleToID[strings.ToLower(target)]
			if !ok {
				continue
			}
			if fromID == toID {
				continue
			}
			if err := s.LinkItems(ctx, fromID, toID, "", false); err != nil {
				continue
			}
			linked++
		}
	}

	if flagJSON {
		printJSON(map[string]int{"imported": imported, "links": linked, "total": len(notes)})
	} else {
		fmt.Printf("\nDone: %d imported, %d links created, %d total notes\n", imported, linked, len(notes))
	}
	return nil
}

func parseObsidianNote(r *os.File, path, vaultRoot string) struct {
	importItem
	wikilinks []string
	filePath  string
} {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	title := strings.TrimSuffix(filepath.Base(path), ".md")

	var tags []string
	var createdAt *time.Time
	bodyStart := 0

	// Parse YAML frontmatter
	if len(lines) > 0 && lines[0] == "---" {
		inTags := false
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				bodyStart = i + 1
				break
			}
			line := strings.TrimSpace(lines[i])

			if strings.HasPrefix(line, "tags:") {
				inTags = true
				val := strings.TrimPrefix(line, "tags:")
				val = strings.TrimSpace(val)
				if val != "" {
					// Inline tags: tags: [tag1, tag2] or tags: tag1, tag2
					val = strings.Trim(val, "[]")
					for _, t := range strings.Split(val, ",") {
						t = strings.TrimSpace(strings.Trim(t, "\"'#"))
						if t != "" {
							tags = append(tags, normalizeTag(t))
						}
					}
					inTags = false
				}
			} else if inTags && strings.HasPrefix(line, "- ") {
				t := strings.TrimPrefix(line, "- ")
				t = strings.TrimSpace(strings.Trim(t, "\"'#"))
				if t != "" {
					tags = append(tags, normalizeTag(t))
				}
			} else {
				inTags = false
			}

			if strings.HasPrefix(line, "title:") {
				val := strings.TrimPrefix(line, "title:")
				val = strings.Trim(strings.TrimSpace(val), "\"'")
				if val != "" {
					title = val
				}
			}
			if strings.HasPrefix(line, "created:") || strings.HasPrefix(line, "date:") {
				var val string
				if strings.HasPrefix(line, "created:") {
					val = strings.TrimPrefix(line, "created:")
				} else {
					val = strings.TrimPrefix(line, "date:")
				}
				val = strings.TrimSpace(val)
				for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
					if t, err := time.Parse(layout, val); err == nil {
						createdAt = &t
						break
					}
				}
			}
		}
	}

	body := strings.Join(lines[bodyStart:], "\n")
	body = strings.TrimSpace(body)

	// Extract [[wikilinks]]
	matches := wikilinkRe.FindAllStringSubmatch(body, -1)
	var wikilinks []string
	seen := map[string]bool{}
	for _, m := range matches {
		target := strings.TrimSpace(m[1])
		lower := strings.ToLower(target)
		if !seen[lower] {
			wikilinks = append(wikilinks, target)
			seen[lower] = true
		}
	}

	// Also collect inline #tags from body
	for _, word := range strings.Fields(body) {
		if strings.HasPrefix(word, "#") && len(word) > 1 {
			t := strings.TrimLeft(word, "#")
			t = strings.TrimRight(t, ".,;:!?")
			if t != "" && !strings.Contains(t, "#") {
				tags = append(tags, normalizeTag(t))
			}
		}
	}

	absPath, _ := filepath.Abs(path)

	return struct {
		importItem
		wikilinks []string
		filePath  string
	}{
		importItem: importItem{
			title:     title,
			body:      body,
			tags:      tags,
			itemType:  model.TypeSnippet,
			createdAt: createdAt,
		},
		wikilinks: wikilinks,
		filePath:  absPath,
	}
}

