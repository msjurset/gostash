package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"

	"github.com/spf13/cobra"
)

const (
	urlCheckConcurrency = 8
	// Many sites block the default Go User-Agent with 403/406. Using a
	// realistic browser UA drastically cuts false-positive broken URLs.
	urlCheckUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check stash for data hygiene issues",
	Long: `Find broken URLs, orphaned files, missing file references, and duplicate content.

  stash check              # run all checks
  stash check --urls       # only check URLs
  stash check --files      # only check file references
  stash check --dupes      # only check for duplicate content
  stash check --stream     # emit newline-delimited JSON events as issues are found`,
	RunE: runCheck,
}

func init() {
	checkCmd.Flags().Bool("urls", false, "Check for broken URLs")
	checkCmd.Flags().Bool("files", false, "Check for orphaned/missing files")
	checkCmd.Flags().Bool("dupes", false, "Check for duplicate content hashes")
	checkCmd.Flags().Bool("stream", false, "Emit NDJSON events progressively as issues are discovered")
	rootCmd.AddCommand(checkCmd)
}

// checkEvent is the NDJSON envelope emitted in --stream mode.
type checkEvent struct {
	Type  string            `json:"type"`
	Phase string            `json:"phase,omitempty"`
	Total int               `json:"total,omitempty"`
	Done  int               `json:"done,omitempty"`
	Issue *model.CheckIssue `json:"issue,omitempty"`
	Group *model.DupeGroup  `json:"group,omitempty"`
	Path  string            `json:"path,omitempty"`
}

// emitter serializes writes so parallel workers can report findings safely.
type emitter struct {
	mu     sync.Mutex
	stream bool
	enc    *json.Encoder
	result model.CheckResult
}

func newEmitter(stream bool) *emitter {
	return &emitter{
		stream: stream,
		enc:    json.NewEncoder(os.Stdout),
	}
}

func (e *emitter) phaseStart(phase string, total int) {
	if !e.stream {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(checkEvent{Type: "phase_start", Phase: phase, Total: total})
}

func (e *emitter) progress(phase string, done, total int) {
	if !e.stream {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(checkEvent{Type: "progress", Phase: phase, Done: done, Total: total})
}

func (e *emitter) brokenURL(issue model.CheckIssue) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stream {
		_ = e.enc.Encode(checkEvent{Type: "broken_url", Issue: &issue})
		return
	}
	e.result.BrokenURLs = append(e.result.BrokenURLs, issue)
}

func (e *emitter) missingFile(issue model.CheckIssue) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stream {
		_ = e.enc.Encode(checkEvent{Type: "missing_file", Issue: &issue})
		return
	}
	e.result.MissingFiles = append(e.result.MissingFiles, issue)
}

func (e *emitter) orphanedFile(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stream {
		_ = e.enc.Encode(checkEvent{Type: "orphaned_file", Path: path})
		return
	}
	e.result.OrphanedFiles = append(e.result.OrphanedFiles, path)
}

func (e *emitter) duplicateGroup(group model.DupeGroup) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stream {
		_ = e.enc.Encode(checkEvent{Type: "duplicate_group", Group: &group})
		return
	}
	e.result.DuplicateHash = append(e.result.DuplicateHash, group)
}

func (e *emitter) done() {
	if !e.stream {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(checkEvent{Type: "done"})
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
	stream, _ := cmd.Flags().GetBool("stream")
	all := !urls && !files && !dupes

	// Stream mode implies JSON output; suppress any human-readable text so
	// stdout is a pure NDJSON stream that consumers can parse line-by-line.
	emit := newEmitter(stream)
	ctx := context.Background()

	if all || files {
		if err := checkFiles(ctx, s, emit); err != nil {
			return fmt.Errorf("file check: %w", err)
		}
	}

	if all || dupes {
		if err := checkDuplicates(ctx, s, emit); err != nil {
			return fmt.Errorf("dupe check: %w", err)
		}
	}

	if all || urls {
		if err := checkURLs(ctx, s, emit); err != nil {
			return fmt.Errorf("url check: %w", err)
		}
	}

	if stream {
		emit.done()
		return nil
	}

	result := emit.result

	if flagJSON {
		printJSON(result)
		return nil
	}

	issues := len(result.OrphanedFiles) + len(result.MissingFiles) + len(result.DuplicateHash) + len(result.BrokenURLs)

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
}, emit *emitter) error {
	emit.phaseStart("files", 0)

	filesDir := config.FilesDir()

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

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100000})
	if err != nil {
		return err
	}

	dbHashes := map[string]bool{}
	for _, item := range items {
		if item.ContentHash == "" {
			continue
		}
		dbHashes[item.ContentHash] = true

		if _, ok := diskHashes[item.ContentHash]; !ok {
			emit.missingFile(model.CheckIssue{
				ID:     item.ID,
				Title:  item.Title,
				Detail: "content_hash " + item.ContentHash[:16] + "… not found on disk",
			})
		}
	}

	for hash, path := range diskHashes {
		if !dbHashes[hash] {
			rel, _ := filepath.Rel(filesDir, path)
			emit.orphanedFile(rel)
		}
	}

	return nil
}

// checkDuplicates finds items sharing the same content hash.
func checkDuplicates(ctx context.Context, s interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
}, emit *emitter) error {
	emit.phaseStart("dupes", 0)

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100000})
	if err != nil {
		return err
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

	for hash, grouped := range byHash {
		if len(grouped) > 1 {
			emit.duplicateGroup(model.DupeGroup{Hash: hash, Items: grouped})
		}
	}
	return nil
}

// checkURLs does a HEAD request on URL-type items to find broken links.
// Requests run on a worker pool so a slow or failing URL does not block the
// rest; each broken URL is emitted independently as soon as it is detected.
func checkURLs(ctx context.Context, s interface {
	ListItems(context.Context, model.ItemFilter) ([]model.Item, error)
}, emit *emitter) error {
	items, err := s.ListItems(ctx, model.ItemFilter{Type: model.TypeURL, Limit: 100000})
	if err != nil {
		return err
	}

	type urlItem struct {
		id, title, url string
	}
	urls := make([]urlItem, 0, len(items))
	for _, item := range items {
		if item.URL != "" {
			urls = append(urls, urlItem{id: item.ID, title: item.Title, url: item.URL})
		}
	}
	items = nil

	if len(urls) == 0 {
		emit.phaseStart("urls", 0)
		return nil
	}

	emit.phaseStart("urls", len(urls))

	if !flagJSON && !emit.stream {
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

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, urlCheckConcurrency)
		doneN    int
		doneMu   sync.Mutex
		doneStep = 25
	)

	bumpDone := func() int {
		doneMu.Lock()
		defer doneMu.Unlock()
		doneN++
		return doneN
	}

	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u urlItem) {
			defer wg.Done()
			defer func() { <-sem }()

			if detail := probeURL(client, u.url); detail != "" {
				emit.brokenURL(model.CheckIssue{ID: u.id, Title: u.title, Detail: detail})
			}

			n := bumpDone()
			if emit.stream {
				emit.progress("urls", n, len(urls))
			} else if !flagJSON && n%doneStep == 0 {
				fmt.Printf("  %d/%d checked...\n", n, len(urls))
			}
		}(u)
	}

	wg.Wait()
	return nil
}

// probeURL returns an empty string if the URL appears healthy, or a short
// failure detail when it is genuinely broken. HEAD is tried first, with a GET
// fallback for sites that refuse HEAD. Statuses that typically indicate
// bot-blocking or rate-limiting rather than a dead URL (401/403/429/451/503)
// are treated as inconclusive and not flagged.
func probeURL(client *http.Client, rawURL string) string {
	if ok, _ := tryRequest(client, http.MethodHead, rawURL); ok {
		return ""
	}
	ok, detail := tryRequest(client, http.MethodGet, rawURL)
	if ok {
		return ""
	}
	return detail
}

// tryRequest performs a single HTTP request and classifies the outcome:
//   - (true, "")       URL is reachable and returned a non-error status
//   - (false, detail)  URL is clearly broken (network failure, 404/410, real 5xx)
//   - (false, "")      Response is inconclusive (auth required, bot-detection,
//     rate limit, legal block, transient unavailability) —
//     we don't flag these as broken.
func tryRequest(client *http.Client, method, rawURL string) (bool, string) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("User-Agent", urlCheckUserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	s := resp.StatusCode
	switch {
	case s < 400:
		return true, ""
	case s == 404, s == 410:
		return false, fmt.Sprintf("HTTP %d", s)
	case s >= 500 && s != 503:
		return false, fmt.Sprintf("HTTP %d", s)
	default:
		// 401, 403, 429, 451, 503, and other 4xx — ambiguous, not flagged.
		return false, ""
	}
}
