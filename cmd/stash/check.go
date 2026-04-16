package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check stash for data hygiene issues",
	Long: `Find broken URLs, orphaned files, missing file references, and duplicate content.

  stash check              # run all checks
  stash check --urls       # only check URLs
  stash check --files      # only check file references
  stash check --dupes      # only check for duplicate content`,
	RunE: runCheck,
}

func init() {
	checkCmd.Flags().Bool("urls", false, "Check for broken URLs")
	checkCmd.Flags().Bool("files", false, "Check for orphaned/missing files")
	checkCmd.Flags().Bool("dupes", false, "Check for duplicate content hashes")
	rootCmd.AddCommand(checkCmd)
}

func runCheck(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	urls, _ := cmd.Flags().GetBool("urls")
	files, _ := cmd.Flags().GetBool("files")
	dupes, _ := cmd.Flags().GetBool("dupes")
	all := !urls && !files && !dupes

	ctx := context.Background()
	result := model.CheckResult{}
	issues := 0

	if all || files {
		orphaned, missing, err := checkFiles(ctx, s)
		if err != nil {
			return fmt.Errorf("file check: %w", err)
		}
		result.OrphanedFiles = orphaned
		result.MissingFiles = missing
		issues += len(orphaned) + len(missing)
	}

	if all || dupes {
		d, err := checkDuplicates(ctx, s)
		if err != nil {
			return fmt.Errorf("dupe check: %w", err)
		}
		result.DuplicateHash = d
		issues += len(d)
	}

	if all || urls {
		broken, err := checkURLs(ctx, s)
		if err != nil {
			return fmt.Errorf("url check: %w", err)
		}
		result.BrokenURLs = broken
		issues += len(broken)
	}

	if flagJSON {
		printJSON(result)
		return nil
	}

	if len(result.OrphanedFiles) > 0 {
		fmt.Printf("Orphaned files (%d) — on disk but not referenced by any item:\n", len(result.OrphanedFiles))
		for _, f := range result.OrphanedFiles {
			fmt.Printf("  %s\n", f)
		}
		fmt.Println()
	}

	if len(result.MissingFiles) > 0 {
		fmt.Printf("Missing files (%d) — referenced by items but not on disk:\n", len(result.MissingFiles))
		for _, m := range result.MissingFiles {
			fmt.Printf("  [%s] %s — %s\n", shortID(m.ID), m.Title, m.Detail)
		}
		fmt.Println()
	}

	if len(result.DuplicateHash) > 0 {
		fmt.Printf("Duplicate content (%d groups):\n", len(result.DuplicateHash))
		for _, g := range result.DuplicateHash {
			fmt.Printf("  hash %s…:\n", g.Hash[:16])
			for _, item := range g.Items {
				fmt.Printf("    [%s] %s\n", shortID(item.ID), item.Title)
			}
		}
		fmt.Println()
	}

	if len(result.BrokenURLs) > 0 {
		fmt.Printf("Broken URLs (%d):\n", len(result.BrokenURLs))
		for _, b := range result.BrokenURLs {
			fmt.Printf("  [%s] %s — %s\n", shortID(b.ID), b.Title, b.Detail)
		}
		fmt.Println()
	}

	if issues == 0 {
		fmt.Println("No issues found.")
	} else {
		fmt.Printf("%d issue(s) found.\n", issues)
	}

	return nil
}

// checkFiles finds orphaned files on disk and items referencing missing files.
func checkFiles(ctx context.Context, s interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
}) (orphaned []string, missing []model.CheckIssue, err error) {
	filesDir := config.FilesDir()

	// Collect all hashes on disk
	diskHashes := map[string]string{} // hash -> path
	filepath.Walk(filesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, ".tmp-") {
			return nil
		}
		diskHashes[name] = path
		return nil
	})

	// Collect all content_hash values from DB
	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100000})
	if err != nil {
		return nil, nil, err
	}

	dbHashes := map[string]bool{}
	for _, item := range items {
		if item.ContentHash == "" {
			continue
		}
		dbHashes[item.ContentHash] = true

		// Check if file exists on disk
		if _, ok := diskHashes[item.ContentHash]; !ok {
			missing = append(missing, model.CheckIssue{
				ID:     item.ID,
				Title:  item.Title,
				Detail: "content_hash " + item.ContentHash[:16] + "… not found on disk",
			})
		}
	}

	// Find orphaned files (on disk but not in DB)
	for hash, path := range diskHashes {
		if !dbHashes[hash] {
			rel, _ := filepath.Rel(filesDir, path)
			orphaned = append(orphaned, rel)
		}
	}

	return orphaned, missing, nil
}

// checkDuplicates finds items sharing the same content hash.
func checkDuplicates(ctx context.Context, s interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
}) ([]model.DupeGroup, error) {
	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100000})
	if err != nil {
		return nil, err
	}

	byHash := map[string][]model.CheckIssue{}
	for _, item := range items {
		if item.ContentHash == "" {
			continue
		}
		byHash[item.ContentHash] = append(byHash[item.ContentHash], model.CheckIssue{
			ID:    item.ID,
			Title: item.Title,
		})
	}

	var groups []model.DupeGroup
	for hash, items := range byHash {
		if len(items) > 1 {
			groups = append(groups, model.DupeGroup{Hash: hash, Items: items})
		}
	}
	return groups, nil
}

// checkURLs does a HEAD request on URL-type items to find broken links.
// Works in batches to avoid holding a database connection open during
// long-running HTTP requests.
func checkURLs(ctx context.Context, s interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
}) ([]model.CheckIssue, error) {
	// Fetch all URL items up front, then release the DB connection
	items, err := s.ListItems(ctx, model.ItemFilter{Type: model.TypeURL, Limit: 100000})
	if err != nil {
		return nil, err
	}

	// Collect only the fields we need so the item slice (and any DB refs) can be GC'd
	type urlItem struct {
		id, title, url string
	}
	urls := make([]urlItem, 0, len(items))
	for _, item := range items {
		if item.URL != "" {
			urls = append(urls, urlItem{id: item.ID, title: item.Title, url: item.URL})
		}
	}
	items = nil // release

	if len(urls) == 0 {
		return nil, nil
	}

	if !flagJSON {
		fmt.Printf("Checking %d URLs...\n", len(urls))
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	var broken []model.CheckIssue
	for i, u := range urls {
		if !flagJSON && (i+1)%25 == 0 {
			fmt.Printf("  %d/%d checked...\n", i+1, len(urls))
		}
		resp, err := client.Head(u.url)
		if err != nil {
			broken = append(broken, model.CheckIssue{
				ID:     u.id,
				Title:  u.title,
				Detail: err.Error(),
			})
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			broken = append(broken, model.CheckIssue{
				ID:     u.id,
				Title:  u.title,
				Detail: fmt.Sprintf("HTTP %d", resp.StatusCode),
			})
		}
	}
	return broken, nil
}
