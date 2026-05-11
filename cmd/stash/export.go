package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/archive"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [ids...]",
	Short: "Export items to a portable zip archive",
	Long: `Bundle items into a single .zip archive containing a manifest.json
plus per-item subdirectories with blobs (file/image originals,
snippet markdown, URL text, thumbnails).

Selection — pick exactly one:

  stash export 01ABC... 01DEF...    # explicit IDs
  stash export -                    # IDs from stdin (one per line)
  stash export --tag fishing        # every item carrying #fishing
  stash export --collection bills   # every item in collection 'bills'
  stash export --all                # every item in the stash

Output goes to --out (default: ./stash-export-YYYY-MM-DD.zip).
The archive is round-trip importable via 'stash import'.`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().String("out", "", "Output zip path (default: ./stash-export-<date>.zip)")
	exportCmd.Flags().String("tag", "", "Export every item carrying this tag")
	exportCmd.Flags().String("collection", "", "Export every item in this collection")
	exportCmd.Flags().Bool("all", false, "Export every item in the stash (ignores --tag / --collection / id args)")
	exportCmd.Flags().Bool("include-archived", false, "Include soft-deleted (archived) items")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	tagFlag, _ := cmd.Flags().GetString("tag")
	collectionFlag, _ := cmd.Flags().GetString("collection")
	allFlag, _ := cmd.Flags().GetBool("all")
	includeArchived, _ := cmd.Flags().GetBool("include-archived")
	outPath, _ := cmd.Flags().GetString("out")

	// Resolve IDs from argv (with `-` as stdin sentinel) so the CLI
	// can take a list too long for shell argv. Mac app uses the
	// stdin form for multi-select exports.
	var explicitIDs []string
	if len(args) > 0 {
		if len(args) == 1 && args[0] == "-" {
			explicitIDs = readStdinIDs(cmd)
		} else {
			explicitIDs = args
		}
	}

	// Exactly one selection mode wins.
	modeCount := 0
	if len(explicitIDs) > 0 {
		modeCount++
	}
	if tagFlag != "" {
		modeCount++
	}
	if collectionFlag != "" {
		modeCount++
	}
	if allFlag {
		modeCount++
	}
	if modeCount == 0 {
		return fmt.Errorf("specify ids, --tag, --collection, or --all")
	}
	if modeCount > 1 {
		return fmt.Errorf("ids, --tag, --collection, and --all are mutually exclusive")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()

	// Resolve the item set.
	var items []model.Item
	var scope archive.Scope
	switch {
	case len(explicitIDs) > 0:
		scope = archive.Scope{Type: "ids"}
		for _, id := range explicitIDs {
			item, err := s.GetItem(ctx, id)
			if err != nil {
				return fmt.Errorf("lookup %s: %w", shortID(id), err)
			}
			if item.Archived && !includeArchived {
				continue
			}
			items = append(items, *item)
		}
	case tagFlag != "":
		scope = archive.Scope{Type: "tag", Value: tagFlag}
		items, err = s.ListItems(ctx, model.ItemFilter{
			Tags:            []string{tagFlag},
			Limit:           0, // unlimited
			IncludeArchived: includeArchived,
		})
		if err != nil {
			return err
		}
	case collectionFlag != "":
		scope = archive.Scope{Type: "collection", Value: collectionFlag}
		items, err = s.ListItems(ctx, model.ItemFilter{
			Collection:      collectionFlag,
			Limit:           0,
			IncludeArchived: includeArchived,
		})
		if err != nil {
			return err
		}
	case allFlag:
		scope = archive.Scope{Type: "all"}
		items, err = s.ListItems(ctx, model.ItemFilter{
			Limit:           0,
			IncludeArchived: includeArchived,
		})
		if err != nil {
			return err
		}
	}

	if len(items) == 0 {
		return fmt.Errorf("no items match the selection")
	}

	if outPath == "" {
		outPath = defaultExportPath(scope)
	}

	result, err := archive.WriteZip(outPath, archive.ExportInput{
		Items:           items,
		Scope:           scope,
		FileStore:       openFileStore(),
		ExporterVersion: version,
	})
	if err != nil {
		return err
	}

	if flagJSON {
		printJSON(result)
	} else {
		fmt.Printf("Exported %d items (%d blobs, %s) → %s\n",
			result.ItemCount, result.BlobCount,
			humanSize(result.TotalBytes), result.Path)
	}
	return nil
}

func defaultExportPath(scope archive.Scope) string {
	stamp := time.Now().Format("2006-01-02")
	switch scope.Type {
	case "tag":
		return fmt.Sprintf("./stash-export-tag-%s-%s.zip", sanitizePathPart(scope.Value), stamp)
	case "collection":
		return fmt.Sprintf("./stash-export-collection-%s-%s.zip", sanitizePathPart(scope.Value), stamp)
	case "all":
		return fmt.Sprintf("./stash-export-all-%s.zip", stamp)
	default:
		return fmt.Sprintf("./stash-export-%s.zip", stamp)
	}
}

// sanitizePathPart strips slashes / spaces / weird chars so a
// tag/collection name can land in a filename safely.
func sanitizePathPart(s string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ' ', '\t', ':':
			return '-'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	if len(clean) > 60 {
		clean = clean[:60]
	}
	if clean == "" {
		return "items"
	}
	return clean
}

// readStdinIDs reads ids from stdin one-per-line, stripping
// whitespace and skipping blanks. Mirrors `stash collection
// reorder -` so CLI users have one consistent stdin convention.
func readStdinIDs(cmd *cobra.Command) []string {
	var out []string
	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

