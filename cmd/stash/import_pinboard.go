package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var importPinboardCmd = &cobra.Command{
	Use:   "pinboard <file>",
	Short: "Import bookmarks from Pinboard JSON export",
	Long: `Import bookmarks from a Pinboard JSON export file.
Export from: pinboard.in/export/

Tags and notes are preserved. Items marked "toread" get a "to-read" tag.`,
	Args: cobra.ExactArgs(1),
	RunE: runImportPinboard,
}

func init() {
	importPinboardCmd.Flags().StringSliceP("tag", "T", nil, "Extra tags to add to all imported items")
	importPinboardCmd.Flags().StringP("collection", "c", "", "Add all imports to this collection")
	importPinboardCmd.Flags().Bool("dry-run", false, "Preview what would be imported without saving")
	importCmd.AddCommand(importPinboardCmd)
}

type pinboardBookmark struct {
	Href        string `json:"href"`
	Description string `json:"description"`
	Extended    string `json:"extended"`
	Tags        string `json:"tags"`
	Time        string `json:"time"`
	ToRead      string `json:"toread"`
}

func runImportPinboard(cmd *cobra.Command, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var pins []pinboardBookmark
	if err := json.Unmarshal(data, &pins); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}

	extraTags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if dryRun {
		fmt.Printf("Found %d Pinboard bookmarks. (dry run)\n\n", len(pins))
		for _, p := range pins {
			fmt.Printf("  %s\n    %s\n", p.Description, p.Href)
			if p.Tags != "" {
				fmt.Printf("    tags: %s\n", p.Tags)
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

	for _, p := range pins {
		if p.Href == "" {
			continue
		}

		exists, err := s.ExistsByURL(ctx, p.Href)
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
		if t, err := time.Parse(time.RFC3339, p.Time); err == nil {
			created = t
		}

		item := &model.Item{
			ID:        id,
			Type:      model.TypeURL,
			Title:     p.Description,
			URL:       p.Href,
			Notes:     p.Extended,
			CreatedAt: created,
			UpdatedAt: now,
			Metadata:  json.RawMessage("{}"),
		}

		// Parse tags
		allTags := map[string]bool{}
		for _, t := range strings.Fields(p.Tags) {
			allTags[normalizeTag(t)] = true
		}
		if p.ToRead == "yes" {
			allTags["to-read"] = true
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
			fmt.Fprintf(os.Stderr, "warning: failed to import %q: %v\n", p.Href, err)
			continue
		}
		imported++

		if !flagJSON {
			fmt.Printf("  Imported [%s] %s\n", shortID(id), p.Description)
		}
	}

	if flagJSON {
		printJSON(map[string]int{"imported": imported, "skipped": skipped, "total": len(pins)})
	} else {
		fmt.Printf("\nDone: %d imported, %d skipped (duplicate), %d total\n", imported, skipped, len(pins))
	}
	return nil
}
