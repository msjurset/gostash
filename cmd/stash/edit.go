package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/msjurset/gostash/internal/audit"
	"github.com/msjurset/gostash/internal/model"
	"github.com/spf13/cobra"
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a stashed item",
	Args:  cobra.ExactArgs(1),
	RunE:  runEdit,
}

func init() {
	editCmd.Flags().StringP("title", "t", "", "New title")
	editCmd.Flags().StringP("note", "n", "", "New note")
	editCmd.Flags().StringP("extracted-text", "e", "", "Set extracted text")
	editCmd.Flags().StringP("url", "u", "", "New URL (for link items — fix dead links by pointing them at the new address)")
	editCmd.Flags().StringSlice("add-tag", nil, "Add tags (repeatable)")
	editCmd.Flags().StringSlice("remove-tag", nil, "Remove tags (repeatable)")
	editCmd.Flags().StringP("collection", "c", "", "Add to collection")
	editCmd.Flags().String("location", "", "Set geolocation as 'lat,lon' (decimal degrees); sets source=manual")
	editCmd.Flags().Bool("clear-location", false, "Remove the item's stored location")
	rootCmd.AddCommand(editCmd)
}

func runEdit(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	id := args[0]

	item, err := s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	if cmd.Flags().Changed("title") {
		item.Title, _ = cmd.Flags().GetString("title")
	}
	if cmd.Flags().Changed("note") {
		item.Notes, _ = cmd.Flags().GetString("note")
	}
	if cmd.Flags().Changed("extracted-text") {
		item.ExtractedText, _ = cmd.Flags().GetString("extracted-text")
	}
	if cmd.Flags().Changed("url") {
		item.URL, _ = cmd.Flags().GetString("url")
	}
	if cmd.Flags().Changed("clear-location") {
		clear, _ := cmd.Flags().GetBool("clear-location")
		if clear {
			item.Location = nil
		}
	}
	if cmd.Flags().Changed("location") {
		raw, _ := cmd.Flags().GetString("location")
		loc, err := parseLocationFlag(raw)
		if err != nil {
			return err
		}
		item.Location = loc
	}

	if err := s.UpdateItem(ctx, item); err != nil {
		return err
	}

	// Handle tag additions
	if addTags, _ := cmd.Flags().GetStringSlice("add-tag"); len(addTags) > 0 {
		for _, t := range addTags {
			if err := s.AddTag(ctx, id, t); err != nil {
				return fmt.Errorf("add tag %q: %w", t, err)
			}
			logTagAudit(item, audit.ActionAdd, t, "edit")
		}
	}

	// Handle tag removals
	if rmTags, _ := cmd.Flags().GetStringSlice("remove-tag"); len(rmTags) > 0 {
		for _, t := range rmTags {
			if err := s.RemoveTag(ctx, id, t); err != nil {
				return fmt.Errorf("remove tag %q: %w", t, err)
			}
			logTagAudit(item, audit.ActionRemove, t, "edit")
		}
	}

	// Handle collection
	if col, _ := cmd.Flags().GetString("collection"); col != "" {
		if err := s.AddToCollection(ctx, id, col); err != nil {
			return fmt.Errorf("add to collection: %w", err)
		}
	}

	// Re-fetch to show updated state
	item, err = s.GetItem(ctx, id)
	if err != nil {
		return err
	}

	if flagJSON {
		printJSON(item)
	} else {
		fmt.Printf("Updated [%s] %s\n", shortID(item.ID), item.Title)
	}
	return nil
}

// parseLocationFlag accepts "lat,lon" (decimal degrees) and returns
// a *Location with Source="manual". Leading/trailing whitespace and
// a surrounding pair of parens are tolerated so users can paste
// "(33.7547, -84.6322)" or "33.7547,-84.6322". Out-of-range or
// non-numeric components are surfaced as errors so a typo doesn't
// silently corrupt the record.
func parseLocationFlag(raw string) (*model.Location, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("--location: expected 'lat,lon', got %q", raw)
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return nil, fmt.Errorf("--location: bad latitude %q: %w", parts[0], err)
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return nil, fmt.Errorf("--location: bad longitude %q: %w", parts[1], err)
	}
	if lat < -90 || lat > 90 {
		return nil, fmt.Errorf("--location: latitude %v out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		return nil, fmt.Errorf("--location: longitude %v out of range [-180, 180]", lon)
	}
	return &model.Location{Lat: lat, Lon: lon, Source: "manual"}, nil
}
