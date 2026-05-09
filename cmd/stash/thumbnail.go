package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
	"github.com/spf13/cobra"
)

// minThumbnailDim is the floor for an "actual" thumbnail. Anything
// smaller is almost certainly a tracking pixel, layout spacer, or
// favicon — not a useful thumbnail. Catches the cases where a CDN
// happily serves a content-type=image/* response for a 1×1 transparent
// GIF (Amazon's `transparent-pixel._V…_.gif`, etc.).
const minThumbnailDim = 80

var thumbnailCmd = &cobra.Command{
	Use:   "thumbnail",
	Short: "Manage per-item thumbnail images",
	Long: `Set, clear, and inspect the per-item thumbnail used in the
detail view, list rows, and (eventually) grid view. The CLI does not
post-process images — callers are expected to hand in a properly sized
artifact (the macOS app runs the saliency-crop / sRGB / JPEG pipeline
before invoking the CLI).`,
}

var thumbnailSetCmd = &cobra.Command{
	Use:   "set <id>",
	Short: "Set an item's thumbnail from a local file or remote URL",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailSet,
}

var thumbnailClearCmd = &cobra.Command{
	Use:   "clear <id>",
	Short: "Remove an item's thumbnail",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailClear,
}

var thumbnailPathCmd = &cobra.Command{
	Use:   "path <id>",
	Short: "Print the absolute path to an item's thumbnail (empty if unset)",
	Args:  cobra.ExactArgs(1),
	RunE:  runThumbnailPath,
}

var thumbnailImportCmd = &cobra.Command{
	Use:   "import <id>",
	Short: "Fetch a URL and import its best thumbnail candidate",
	Long: `Fetch a URL and import its best thumbnail. The source URL defaults
to the item's own URL but --from overrides — useful for harvesting an
image from a different page or a direct image URL when the stashed
link doesn't have a great hero image.

Response branching:
  image/*   → use the response body directly
  text/html → parse og:image, twitter:image, schema.org image,
              apple-touch-icon, and in-page <img>; pick the
              highest-scoring candidate.

--candidates prints the ranked list as JSON without persisting, for
callers (e.g. the Mac picker sheet) that want to let the user choose.`,
	Args: cobra.ExactArgs(1),
	RunE: runThumbnailImport,
}

func init() {
	thumbnailSetCmd.Flags().String("file", "", "Local file to copy in as the thumbnail")
	thumbnailSetCmd.Flags().String("url", "", "Remote image URL to download")
	thumbnailImportCmd.Flags().String("from", "", "Source URL (defaults to item.url)")
	thumbnailImportCmd.Flags().Bool("candidates", false, "Print ranked candidates as JSON; do not persist")
	thumbnailCmd.AddCommand(thumbnailSetCmd)
	thumbnailCmd.AddCommand(thumbnailImportCmd)
	thumbnailCmd.AddCommand(thumbnailClearCmd)
	thumbnailCmd.AddCommand(thumbnailPathCmd)
	rootCmd.AddCommand(thumbnailCmd)
}

func runThumbnailSet(cmd *cobra.Command, args []string) error {
	filePath, _ := cmd.Flags().GetString("file")
	urlStr, _ := cmd.Flags().GetString("url")
	if filePath == "" && urlStr == "" {
		return fmt.Errorf("--file or --url required")
	}
	if filePath != "" && urlStr != "" {
		return fmt.Errorf("--file and --url are mutually exclusive")
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}

	fs := openFileStore()
	thumbsDir := filepath.Join(fs.BaseDir(), "thumbnails")
	if err := os.MkdirAll(thumbsDir, 0755); err != nil {
		return fmt.Errorf("create thumbnails dir: %w", err)
	}

	var srcReader io.ReadCloser
	var ext string
	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("open %s: %w", filePath, err)
		}
		srcReader = f
		ext = strings.ToLower(filepath.Ext(filePath))
	} else {
		ext, srcReader, err = downloadImage(urlStr)
		if err != nil {
			return err
		}
	}
	defer srcReader.Close()

	if ext == "" {
		ext = ".jpg"
	}

	// Remove any previous thumbnail file for this item before writing
	// the new one — the extension may differ.
	if item.ThumbnailPath != "" {
		fs.RemoveRelative(item.ThumbnailPath)
	}

	relPath := filepath.Join("thumbnails", item.ID+ext)
	dest := filepath.Join(fs.BaseDir(), relPath)
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create thumbnail file: %w", err)
	}
	if _, err := io.Copy(out, srcReader); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("write thumbnail: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return fmt.Errorf("close thumbnail: %w", err)
	}

	item.ThumbnailPath = relPath
	if err := s.UpdateItem(ctx, item); err != nil {
		os.Remove(dest)
		return fmt.Errorf("update item: %w", err)
	}

	if flagJSON {
		printJSON(map[string]string{
			"id":             item.ID,
			"thumbnail_path": relPath,
			"abs_path":       dest,
		})
	} else {
		fmt.Printf("Set thumbnail for [%s] %s\n", shortID(item.ID), dest)
	}
	return nil
}

func runThumbnailClear(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	if item.ThumbnailPath == "" {
		if flagJSON {
			printJSON(map[string]string{"id": item.ID, "thumbnail_path": ""})
		} else {
			fmt.Printf("[%s] no thumbnail set\n", shortID(item.ID))
		}
		return nil
	}

	fs := openFileStore()
	if err := fs.RemoveRelative(item.ThumbnailPath); err != nil {
		return err
	}
	item.ThumbnailPath = ""
	if err := s.UpdateItem(ctx, item); err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	if flagJSON {
		printJSON(map[string]string{"id": item.ID, "thumbnail_path": ""})
	} else {
		fmt.Printf("Cleared thumbnail for [%s]\n", shortID(item.ID))
	}
	return nil
}

func runThumbnailPath(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}
	if item.ThumbnailPath == "" {
		return nil
	}
	fs := openFileStore()
	fmt.Println(fs.ResolveRelative(item.ThumbnailPath))
	return nil
}

// downloadImage fetches a remote URL and returns its extension (with
// leading dot, lower-case) plus a reader for the body. The caller
// owns Close. Extension is derived from the URL path or the
// Content-Type, falling back to ".jpg" when neither resolves.
func downloadImage(rawURL string) (string, io.ReadCloser, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return "", nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if ext == "" {
		ext = extFromContentType(resp.Header.Get("Content-Type"))
	}
	return ext, resp.Body, nil
}

// extFromContentType returns a "." + extension for a known image
// Content-Type, falling back to ".jpg".
func extFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".jpg"
	}
}

// fetchHTTP performs a GET with a browser User-Agent and returns the
// response body + Content-Type. Caller owns Close.
//
// `referer` is set as the `Referer` header when non-empty. This is
// load-bearing for image-CDN candidates: most hot-link protection
// schemes serve images only when the Referer matches the origin
// site. Without it, requests for og:image candidates from sites
// like TripAdvisor, Mystery Tackle Box, etc. hit 403/502.
func fetchHTTP(rawURL, referer string) (io.ReadCloser, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,image/*;q=0.9,*/*;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// runThumbnailImport implements `stash thumbnail import` — the
// URL-driven harvest path used by the Mac app's "Import Thumbnail"
// flow and the rule engine's `set_thumbnail: { from: ... }` action.
func runThumbnailImport(cmd *cobra.Command, args []string) error {
	fromURL, _ := cmd.Flags().GetString("from")
	candidatesOnly, _ := cmd.Flags().GetBool("candidates")

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx := context.Background()
	item, err := s.GetItem(ctx, args[0])
	if err != nil {
		return err
	}

	if fromURL == "" {
		fromURL = item.URL
	}
	if fromURL == "" {
		return fmt.Errorf("no source URL: pass --from or use a URL-typed item")
	}

	if candidatesOnly {
		return runThumbnailCandidates(item, fromURL)
	}
	result, err := importThumbnailForItem(s, item, fromURL)
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(map[string]any{
			"id":             item.ID,
			"thumbnail_path": result.RelPath,
			"source":         result.Source,
			"candidate_url":  result.CandidateURL,
		})
	} else {
		if result.Source == "direct" {
			fmt.Printf("Imported thumbnail for [%s]\n", shortID(item.ID))
		} else {
			fmt.Printf("Imported thumbnail for [%s] from %s (%s)\n",
				shortID(item.ID), result.CandidateURL, result.Source)
		}
	}
	return nil
}

func runThumbnailCandidates(item *model.Item, fromURL string) error {
	body, ct, err := fetchHTTP(fromURL, "")
	if err != nil {
		return err
	}
	defer body.Close()
	if strings.HasPrefix(strings.ToLower(ct), "image/") {
		printJSON([]extract.ThumbnailCandidate{
			{URL: fromURL, Source: "direct", Score: 1},
		})
		return nil
	}
	if !strings.Contains(strings.ToLower(ct), "html") {
		return fmt.Errorf("unsupported content-type %q", ct)
	}
	htmlBytes, err := io.ReadAll(io.LimitReader(body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read html: %w", err)
	}
	cands, err := extract.ExtractThumbnailCandidates(bytes.NewReader(htmlBytes), fromURL)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	printJSON(cands)
	return nil
}

// ThumbnailImportResult describes a successful import. Used by the
// CLI handler for output and by the rules engine for activity logs.
type ThumbnailImportResult struct {
	RelPath      string
	Source       string // "direct", "og", "twitter", "schema", "apple-touch", "in-page"
	CandidateURL string // the actual image URL we downloaded
}

// importThumbnailForItem fetches `fromURL`, branches on Content-Type,
// and persists the resulting image as the item's thumbnail. Used by
// both `stash thumbnail import` and the rules engine `set_thumbnail`
// action — keeping the logic in one place means HTML scraping, the
// candidate-walk fallback, hot-link Referer handling, and CDN failure
// modes all behave identically across surfaces.
func importThumbnailForItem(s store.Store, item *model.Item, fromURL string) (ThumbnailImportResult, error) {
	body, ct, err := fetchHTTP(fromURL, "")
	if err != nil {
		return ThumbnailImportResult{}, err
	}
	defer body.Close()

	fs := openFileStore()

	// Direct image — read and persist, no scrape needed.
	if strings.HasPrefix(strings.ToLower(ct), "image/") {
		ext := extFromContentType(ct)
		relPath, err := writeThumbnail(s, fs, item, body, ext)
		if err != nil {
			return ThumbnailImportResult{}, err
		}
		return ThumbnailImportResult{RelPath: relPath, Source: "direct", CandidateURL: fromURL}, nil
	}

	if !strings.Contains(strings.ToLower(ct), "html") {
		return ThumbnailImportResult{}, fmt.Errorf("unsupported content-type %q", ct)
	}

	// HTML — buffer, parse, walk candidates in rank order. 10MB cap
	// matches internal/fetch.URL. Try up to 5 candidates because CDN
	// flakes / 403 hot-link blocks / 404 stale og:images are common.
	htmlBytes, err := io.ReadAll(io.LimitReader(body, 10*1024*1024))
	if err != nil {
		return ThumbnailImportResult{}, fmt.Errorf("read html: %w", err)
	}
	cands, err := extract.ExtractThumbnailCandidates(bytes.NewReader(htmlBytes), fromURL)
	if err != nil {
		return ThumbnailImportResult{}, fmt.Errorf("extract: %w", err)
	}
	if len(cands) == 0 {
		return ThumbnailImportResult{}, fmt.Errorf("no thumbnail candidates found at %s", fromURL)
	}

	const maxAttempts = 5
	limit := len(cands)
	if limit > maxAttempts {
		limit = maxAttempts
	}
	var lastErr error
	rejected := 0
	for i := 0; i < limit; i++ {
		cand := cands[i]
		imgBody, imgCT, fetchErr := fetchHTTP(cand.URL, fromURL)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		if !strings.HasPrefix(strings.ToLower(imgCT), "image/") {
			imgBody.Close()
			lastErr = fmt.Errorf("not an image (%q): %s", imgCT, cand.URL)
			continue
		}
		// Buffer the body so we can decode dimensions before persisting.
		// Spacer-pixel CDN responses are <100 bytes; even oversized
		// product photos sit under a few MB, well below the 10MB cap on
		// the page fetch above. Reject undersized images outright so
		// callers (rules engine, Mac WebKit fallback chain) see "no
		// thumbnail candidates" rather than a content-type-valid junk
		// image masking a real failure.
		buf, readErr := io.ReadAll(io.LimitReader(imgBody, 10*1024*1024))
		imgBody.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read candidate body: %w", readErr)
			continue
		}
		cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(buf))
		if decodeErr != nil {
			lastErr = fmt.Errorf("decode candidate: %w", decodeErr)
			continue
		}
		if cfg.Width < minThumbnailDim || cfg.Height < minThumbnailDim {
			rejected++
			lastErr = fmt.Errorf("candidate too small (%dx%d): %s", cfg.Width, cfg.Height, cand.URL)
			continue
		}
		ext := extFromContentType(imgCT)
		relPath, writeErr := writeThumbnail(s, fs, item, bytes.NewReader(buf), ext)
		if writeErr != nil {
			lastErr = writeErr
			continue
		}
		return ThumbnailImportResult{RelPath: relPath, Source: cand.Source, CandidateURL: cand.URL}, nil
	}
	// When every candidate was a tracking pixel / layout spacer, surface
	// the same error the empty-candidates path uses. The Mac fallback
	// chain branches on this exact substring to fire WKWebView, which
	// is the right next step here (Amazon SPAs, etc.).
	if rejected > 0 && rejected == limit {
		return ThumbnailImportResult{}, fmt.Errorf("no thumbnail candidates found at %s (all rejected as too small)", fromURL)
	}
	return ThumbnailImportResult{}, fmt.Errorf("could not fetch any candidate (last: %v)", lastErr)
}

// writeThumbnail reads `src`, writes it to the filestore at
// thumbnails/<id><ext>, and updates the item's thumbnail_path
// column. Removes any pre-existing thumbnail file (which may have a
// different extension) before writing.
func writeThumbnail(
	s store.Store,
	fs *filestore.FileStore,
	item *model.Item,
	src io.Reader,
	ext string,
) (string, error) {
	if ext == "" {
		ext = ".jpg"
	}
	thumbsDir := filepath.Join(fs.BaseDir(), "thumbnails")
	if err := os.MkdirAll(thumbsDir, 0755); err != nil {
		return "", fmt.Errorf("create thumbnails dir: %w", err)
	}
	if item.ThumbnailPath != "" {
		fs.RemoveRelative(item.ThumbnailPath)
	}
	relPath := filepath.Join("thumbnails", item.ID+ext)
	dest := filepath.Join(fs.BaseDir(), relPath)
	out, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("create thumbnail file: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dest)
		return "", fmt.Errorf("write thumbnail: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("close thumbnail: %w", err)
	}
	item.ThumbnailPath = relPath
	if err := s.UpdateItem(context.Background(), item); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("update item: %w", err)
	}
	return relPath, nil
}
