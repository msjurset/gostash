package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
)

var migrateNotesLocationCmd = &cobra.Command{
	Use:   "migrate-location-from-notes [id]",
	Short: "Lift '📍 https://maps...' coordinates out of Notes into the Location field",
	Long: `Scans item Notes for the legacy '📍 https://maps.google.com/?q=lat,lon'
pin pattern the mobile app used to embed before the structured
Location field existed. Parses the coordinates, writes them to
item.Location with source="capture" (the data originally came from
the mobile OS Location API), and by default removes the matched line
from Notes so the data isn't duplicated.

Items that already have a Location are skipped — the structured
field wins. Use --force to overwrite existing capture-sourced
locations from Notes; manual / exif sources are always preserved.

Default behavior strips the matched line from Notes since it's
strictly redundant once Location is populated. Pass --keep-line to
retain it.

Examples:
  stash migrate-location-from-notes --all --dry-run
  stash migrate-location-from-notes --all
  stash migrate-location-from-notes 01KRRYH2TP
  stash migrate-location-from-notes --all --keep-line  # preserve Notes lines`,
	RunE: runMigrateNotesLocation,
}

func init() {
	migrateNotesLocationCmd.Flags().Bool("all", false, "Scan every item with a 📍 pattern in Notes")
	migrateNotesLocationCmd.Flags().Bool("dry-run", false, "Report what would change without writing")
	migrateNotesLocationCmd.Flags().Bool("force", false, "Overwrite existing capture-sourced locations (manual / exif always preserved)")
	migrateNotesLocationCmd.Flags().Bool("keep-line", false, "Keep the 📍 line in Notes after lifting the coordinates")
	rootCmd.AddCommand(migrateNotesLocationCmd)
}

// notesLocationRegex matches the mobile-app pattern:
//
//	📍 https://maps.google.com/?q=33.7544,-84.6272
//
// Tolerant of:
//   - either maps.google.com or maps.apple.com host
//   - http or https scheme
//   - whitespace before the URL ("📍 " or "📍   ")
//   - the URL appearing later in the line preceded by other text
//   - the q= value appearing among other URL params
//
// Capture groups: 1=lat, 2=lon. Values are validated downstream
// (range, NaN check) so the regex stays simple.
var notesLocationRegex = regexp.MustCompile(`📍\s*\S*https?://[^\s]*[?&]q=(-?\d+\.?\d*)\s*,\s*(-?\d+\.?\d*)`)

func runMigrateNotesLocation(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	keepLine, _ := cmd.Flags().GetBool("keep-line")

	if all && len(args) > 0 {
		return fmt.Errorf("specify either <id> or --all, not both")
	}
	if !all && len(args) == 0 {
		return fmt.Errorf("specify an item id or pass --all")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	targets, err := migrateNotesTargets(ctx, s, args, all, force)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if flagJSON {
			printJSON(map[string]any{"updated": 0, "skipped": 0, "errors": 0})
		} else {
			fmt.Println("Nothing to migrate.")
		}
		return nil
	}

	if dryRun {
		if !flagJSON {
			fmt.Printf("Would migrate %d item(s):\n", len(targets))
		}
		dry := make([]map[string]any, 0, len(targets))
		for _, t := range targets {
			lat, lon, _, ok := parseNotesLocation(t.Notes)
			if !ok {
				continue
			}
			if flagJSON {
				dry = append(dry, map[string]any{
					"id":    t.ID,
					"title": t.Title,
					"lat":   lat,
					"lon":   lon,
				})
			} else {
				fmt.Printf("  [%s] %s → %.6f, %.6f\n", shortID(t.ID), t.Title, lat, lon)
			}
		}
		if flagJSON {
			printJSON(map[string]any{"would_migrate": dry})
		}
		return nil
	}

	setCount, keptCount, forcedCount := 0, 0, 0
	var errs []string
	for _, item := range targets {
		tag, lat, lon, err := migrateOneNotesLocation(ctx, s, item, keepLine, force)
		if err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", shortID(item.ID), err))
			if !flagJSON {
				fmt.Printf("  ✗ [%s] %s — %v\n", shortID(item.ID), item.Title, err)
			}
			continue
		}
		switch tag {
		case "set":
			setCount++
			if !flagJSON {
				fmt.Printf("  ✓ [%s] %s → set %.6f, %.6f\n", shortID(item.ID), item.Title, lat, lon)
			}
		case "kept":
			keptCount++
			if !flagJSON {
				fmt.Printf("  · [%s] %s — kept existing location, line stripped\n", shortID(item.ID), item.Title)
			}
		case "forced":
			forcedCount++
			if !flagJSON {
				fmt.Printf("  ↻ [%s] %s → overwrote with %.6f, %.6f\n", shortID(item.ID), item.Title, lat, lon)
			}
		}
	}

	if flagJSON {
		printJSON(map[string]any{
			"set":    setCount,
			"kept":   keptCount,
			"forced": forcedCount,
			"errors": len(errs),
		})
	} else {
		fmt.Printf("\nSet %d, kept %d, forced %d, %d error(s).\n", setCount, keptCount, forcedCount, len(errs))
		if !keepLine {
			fmt.Println("Notes lines stripped. Pass --keep-line on a future run to preserve.")
		}
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d error(s)", len(errs))
	}
	return nil
}

func migrateNotesTargets(ctx context.Context, s store.Store, args []string, all, force bool) ([]model.Item, error) {
	if !all {
		item, err := s.GetItem(ctx, args[0])
		if err != nil {
			return nil, err
		}
		if !migrateNotesEligible(*item, force) {
			return nil, fmt.Errorf("item has no migratable 📍 pattern in Notes (or already has a location and --force not set)")
		}
		return []model.Item{*item}, nil
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 0})
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var targets []model.Item
	for _, item := range items {
		if migrateNotesEligible(item, force) {
			targets = append(targets, item)
		}
	}
	return targets, nil
}

// migrateNotesEligible: the migration's *primary* purpose is
// cleaning the legacy 📍 line out of Notes now that Location is a
// first-class field. Populating Location is the secondary benefit
// (only when it's currently empty). So eligibility is just "has a
// 📍 URL in Notes" — what we do per-item branches on whether the
// item already has a location and how it got there.
func migrateNotesEligible(item model.Item, force bool) bool {
	return notesLocationRegex.MatchString(item.Notes)
}

// migrateOneNotesLocation handles a single matched item. Three
// possible outcomes per item, all signalled in the returned tag:
//
//   - "set"    — Location was empty; we wrote it from the Notes URL.
//   - "kept"   — Location was already set; we left it alone but
//                still stripped the line (unless --keep-line).
//   - "forced" — --force overwrote an existing capture source.
//
// The Notes-line strip runs in every branch (unless --keep-line)
// because that's the migration's primary purpose: the structured
// field is now the source of truth and the Notes copy is
// redundant.
func migrateOneNotesLocation(ctx context.Context, s store.Store, item model.Item, keepLine, force bool) (tag string, lat, lon float64, err error) {
	lat, lon, line, parsed := parseNotesLocation(item.Notes)
	if !parsed {
		return "", 0, 0, nil
	}
	tag = "kept"
	switch {
	case item.Location == nil:
		item.Location = &model.Location{Lat: lat, Lon: lon, Source: "capture"}
		tag = "set"
	case force && item.Location.Source == "capture":
		item.Location = &model.Location{Lat: lat, Lon: lon, Source: "capture"}
		tag = "forced"
	}
	if !keepLine && line != "" {
		item.Notes = stripLineFromNotes(item.Notes, line)
	}
	if err := s.UpdateItem(ctx, &item); err != nil {
		return "", 0, 0, fmt.Errorf("update: %w", err)
	}
	return tag, lat, lon, nil
}

// parseNotesLocation runs the regex, parses the captured floats,
// and returns the trimmed source line so the caller can remove it
// from Notes. parsed=false when the regex didn't match or the
// floats failed validation (out-of-range / NaN); the migration
// then silently skips the item.
func parseNotesLocation(notes string) (lat, lon float64, line string, parsed bool) {
	m := notesLocationRegex.FindStringSubmatchIndex(notes)
	if m == nil {
		return 0, 0, "", false
	}
	latStr := notes[m[2]:m[3]]
	lonStr := notes[m[4]:m[5]]
	la, errLa := strconv.ParseFloat(latStr, 64)
	lo, errLo := strconv.ParseFloat(lonStr, 64)
	if errLa != nil || errLo != nil {
		return 0, 0, "", false
	}
	if la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, "", false
	}
	// Walk back/forward to the enclosing line so the caller can
	// strip the entire 📍 row, not just the URL substring.
	lineStart := strings.LastIndex(notes[:m[0]], "\n") + 1
	lineEnd := m[1]
	if nl := strings.Index(notes[m[1]:], "\n"); nl >= 0 {
		lineEnd = m[1] + nl
	} else {
		lineEnd = len(notes)
	}
	line = notes[lineStart:lineEnd]
	return la, lo, line, true
}

// stripLineFromNotes removes the matched line and any surrounding
// blank lines so a stripped 📍 row doesn't leave a visible gap.
func stripLineFromNotes(notes, line string) string {
	idx := strings.Index(notes, line)
	if idx < 0 {
		return notes
	}
	start := idx
	end := idx + len(line)
	// Eat the trailing newline if present.
	if end < len(notes) && notes[end] == '\n' {
		end++
	} else if start > 0 && notes[start-1] == '\n' {
		// No trailing newline; eat the leading one instead so the
		// removed line doesn't leave a blank gap.
		start--
	}
	out := notes[:start] + notes[end:]
	// Collapse any consecutive blank lines down to one.
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	// Trim newlines on BOTH sides — when the pin was at the top of
	// Notes, the strip leaves a leading "\n"; at the bottom, a
	// trailing one. Either side is dead space the user doesn't want.
	return strings.Trim(out, "\n")
}
