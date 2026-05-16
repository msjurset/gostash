package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
	"golang.org/x/net/html"
)

const fetchUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

var fetchURLCmd = &cobra.Command{
	Use:   "fetch-url <url>",
	Short: "Pull files referenced by a URL into the stash",
	Long: `Three modes:

  Direct file URL (e.g. https://.../report.pdf, https://.../photo.jpg)
    Downloads the body and stashes it as a file or image item.

      stash fetch-url https://example.com/report.pdf

  HTML page URL — list mode (default for HTML)
    Walks the page for embedded images and direct-file links and
    emits the candidates as JSON. Pick the ones you want with a
    second invocation.

      stash fetch-url https://example.com/article --json

  HTML page URL — pick mode
    Downloads the URLs given via --pick (one or many) and stashes
    each. Pass --link-source to tie the new items to the source
    page item (an existing URL item, or a fresh one is created if
    the page URL isn't in the stash yet). Pass --archive to bundle
    all picks into a single zip-typed file item instead of N items.

      stash fetch-url https://example.com/article \
        --pick https://example.com/img1.jpg \
        --pick https://example.com/img2.jpg \
        --link-source https://example.com/article

Tags / collection flags apply to every newly created item.`,
	Args: cobra.ExactArgs(1),
	RunE: runFetchURL,
}

func init() {
	fetchURLCmd.Flags().Bool("list", false, "Always list candidates, even when the URL points at a file")
	fetchURLCmd.Flags().StringSlice("pick", nil, "Download a specific URL (repeatable). Triggers pick mode.")
	fetchURLCmd.Flags().String("link-source", "", "URL or item id of the source page; new items are linked to it (auto-creates a URL item if needed)")
	fetchURLCmd.Flags().Bool("clique", false, "Also cross-link every picked item to every other picked item (rim edges) — turns the batch into a clique. Quadratic in the pick count; warns past 15 picks.")
	fetchURLCmd.Flags().Bool("archive", false, "When picking many URLs, bundle them as a single zip-typed file item")
	fetchURLCmd.Flags().StringSliceP("tag", "T", nil, "Tags to add to every new item (repeatable)")
	fetchURLCmd.Flags().StringP("collection", "c", "", "Add every new item to this collection")
	fetchURLCmd.Flags().Bool("all-links", false, "In list mode, include every <a href> not just file-extension matches")
	rootCmd.AddCommand(fetchURLCmd)
}

func runFetchURL(cmd *cobra.Command, args []string) error {
	pageURL := args[0]
	listOnly, _ := cmd.Flags().GetBool("list")
	picks, _ := cmd.Flags().GetStringSlice("pick")
	linkSource, _ := cmd.Flags().GetString("link-source")
	clique, _ := cmd.Flags().GetBool("clique")
	asArchive, _ := cmd.Flags().GetBool("archive")
	tags, _ := cmd.Flags().GetStringSlice("tag")
	collection, _ := cmd.Flags().GetString("collection")
	allLinks, _ := cmd.Flags().GetBool("all-links")

	if len(picks) > 0 {
		return runPickMode(pageURL, picks, linkSource, clique, asArchive, tags, collection)
	}

	body, ctype, finalURL, err := fetchURLBytes(pageURL, "")
	if err != nil {
		return err
	}

	mainType := strings.ToLower(strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0]))
	if mainType == "text/html" || (listOnly && strings.HasPrefix(mainType, "text/")) {
		// HTML page → enumerate file candidates.
		page, err := scrapeCandidates(finalURL, body, allLinks)
		if err != nil {
			return err
		}
		return printJSONOrText(page)
	}

	// Direct-file URL. If --list was passed, just describe; otherwise
	// stash it and emit the new item.
	if listOnly {
		return printJSONOrText(directFileCandidate(finalURL, ctype, int64(len(body))))
	}
	return runDirectStash(finalURL, ctype, body, tags, collection)
}

// MARK: - Output shapes

type pageCandidate struct {
	URL   string `json:"url"`
	Label string `json:"label"`           // alt text or filename guess
	MIME  string `json:"mime,omitempty"`  // populated when known (img usually unknown until HEAD)
	Size  int64  `json:"size,omitempty"`  // ditto
	Kind  string `json:"kind"`            // "image" | "link"
}

type pageScrape struct {
	Type       string          `json:"type"` // "page"
	PageURL    string          `json:"page_url"`
	PageTitle  string          `json:"page_title,omitempty"`
	Candidates []pageCandidate `json:"candidates"`
}

type directScrape struct {
	Type  string `json:"type"` // "direct"
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	MIME  string `json:"mime"`
	Size  int64  `json:"size"`
}

type pickedItem struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type pickResult struct {
	Type        string       `json:"type"` // "picked"
	Imported    []pickedItem `json:"imported"`
	LinkedTo    string       `json:"linked_to,omitempty"`
	CliqueEdges int          `json:"clique_edges,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
}

// MARK: - HTML scraping

// stashableExtensions are the file-ish extensions we always treat as
// pick-worthy in --pick mode default. `--all-links` widens this to
// every `<a href>`.
var stashableExtensions = map[string]bool{
	// Documents
	".pdf": true, ".doc": true, ".docx": true, ".rtf": true,
	".ppt": true, ".pptx": true, ".xls": true, ".xlsx": true,
	".csv": true, ".tsv": true, ".txt": true, ".md": true,
	".epub": true, ".mobi": true,
	// Archives
	".zip": true, ".tar": true, ".gz": true, ".tgz": true,
	".rar": true, ".7z": true, ".bz2": true,
	// Images (some pages link to full-size in <a>)
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".heic": true, ".svg": true, ".bmp": true, ".tiff": true,
	// Audio / video
	".mp3": true, ".wav": true, ".flac": true, ".ogg": true, ".m4a": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true,
	// Data
	".json": true, ".xml": true, ".yaml": true, ".yml": true,
	".iso":  true, ".dmg": true,
}

func scrapeCandidates(pageURL string, body []byte, allLinks bool) (pageScrape, error) {
	base, _ := url.Parse(pageURL)
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return pageScrape{}, fmt.Errorf("parse html: %w", err)
	}

	var title string
	seen := map[string]bool{}
	var cands []pageCandidate

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if n.FirstChild != nil && title == "" {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "img":
				src := pickLargestImageURL(n, base)
				if src != "" && !seen[src] {
					seen[src] = true
					cands = append(cands, pageCandidate{
						URL:   src,
						Label: imgLabel(n, src),
						Kind:  "image",
					})
				}
			case "a":
				href := absURL(getAttr(n, "href"), base)
				if href == "" || seen[href] {
					return
				}
				if !allLinks && !looksStashable(href) {
					return
				}
				seen[href] = true
				cands = append(cands, pageCandidate{
					URL:   href,
					Label: candidateLinkLabel(n, href),
					Kind:  "link",
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// Group: images first (most common case), then links. Stable
	// otherwise so the order on disk roughly matches the page order.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Kind == cands[j].Kind {
			return false
		}
		return cands[i].Kind == "image"
	})

	return pageScrape{
		Type:       "page",
		PageURL:    pageURL,
		PageTitle:  title,
		Candidates: cands,
	}, nil
}

// pickLargestImageURL prefers the largest entry in `srcset` over
// `src`. CDNs typically expose multiple resolutions in srcset; the
// `src` is often a low-res placeholder.
func pickLargestImageURL(n *html.Node, base *url.URL) string {
	if ss := getAttr(n, "srcset"); ss != "" {
		if best := largestSrcset(ss); best != "" {
			return absURL(best, base)
		}
	}
	return absURL(getAttr(n, "src"), base)
}

func largestSrcset(ss string) string {
	var bestURL string
	var bestW int
	for _, part := range strings.Split(ss, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) == 0 {
			continue
		}
		u := fields[0]
		w := 0
		if len(fields) > 1 && strings.HasSuffix(fields[1], "w") {
			w, _ = strconv.Atoi(strings.TrimSuffix(fields[1], "w"))
		}
		if bestURL == "" || w > bestW {
			bestURL = u
			bestW = w
		}
	}
	return bestURL
}

func imgLabel(n *html.Node, src string) string {
	if alt := strings.TrimSpace(getAttr(n, "alt")); alt != "" {
		return alt
	}
	return basenameFromURL(src)
}

func candidateLinkLabel(n *html.Node, href string) string {
	if text := strings.TrimSpace(extractText(n)); text != "" {
		return text
	}
	return basenameFromURL(href)
}

func basenameFromURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		base := filepath.Base(u.Path)
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	return raw
}

func looksStashable(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	return stashableExtensions[ext]
}

func absURL(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func directFileCandidate(u, ctype string, size int64) directScrape {
	return directScrape{
		Type:  "direct",
		URL:   u,
		Title: basenameFromURL(u),
		MIME:  ctype,
		Size:  size,
	}
}

// MARK: - Direct fetch + stash

func runDirectStash(srcURL, ctype string, body []byte, tags []string, collection string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()

	item, err := stashFetchedBytes(s, fs, srcURL, ctype, body, tags, collection, "")
	if err != nil {
		return err
	}
	if flagJSON {
		printJSON(pickResult{
			Type: "picked",
			Imported: []pickedItem{{
				ID: item.ID, URL: srcURL, Title: item.Title, Type: string(item.Type),
			}},
		})
	} else {
		fmt.Printf("Stashed [%s] %s (%s, %s)\n",
			shortID(item.ID), item.Title, item.MimeType, humanSize(item.FileSize))
	}
	return nil
}

// stashFetchedBytes turns a freshly-downloaded blob into an item +
// filestore content_hash. Title hint takes precedence (used by the
// Chrome extension to pass alt text or page-derived labels); falls
// back to URL basename. Item type is image/* → image, everything
// else → file. Tags and collection associations applied as a final
// step.
//
// `titleHint` may be empty for the CLI path that has nothing better
// to suggest. When non-empty the URL basename is preserved as a
// SourcePath suffix so Mac UI extension-based hints (e.g. ".jpg"
// → image preview) keep working even when the URL itself has no
// extension (Gemini's lh3.googleusercontent.com/gg/<token> case).
func stashFetchedBytes(
	s store.Store,
	fs *filestore.FileStore,
	srcURL, ctype string,
	body []byte,
	tags []string,
	collection string,
	titleHint string,
) (*model.Item, error) {
	// Apply URL-exclusion rules so a Gemini-chat-style transient
	// source URL doesn't pollute the captured item's URL column.
	// The original srcURL is still used for the fetch + hashing
	// upstream of this call; only what we persist on the item is
	// redacted.
	redactedSrc, _ := config.RedactURL(srcURL)
	item := &model.Item{
		ID:        newFetchID(),
		URL:       redactedSrc,
		Metadata:  json.RawMessage("{}"),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	// Detect MIME from server header first (more reliable for
	// CDNs than sniffing); fall back to sniffing if header is
	// absent or text/plain (common false negative).
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0]))
	if mt == "" || mt == "application/octet-stream" || mt == "text/plain" {
		mt = extract.DetectMIME(body, basenameFromURL(srcURL))
	}
	item.MimeType = mt
	if strings.HasPrefix(mt, "image/") {
		item.Type = model.TypeImage
	} else {
		item.Type = model.TypeFile
	}

	// Title preference: caller-provided hint > URL basename > URL.
	cleanedHint := strings.TrimSpace(titleHint)
	switch {
	case cleanedHint != "":
		item.Title = cleanedHint
	default:
		item.Title = basenameFromURL(srcURL)
		if item.Title == "" || item.Title == "/" {
			item.Title = srcURL
		}
	}

	hash, size, err := fs.Save(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("store blob: %w", err)
	}
	item.ContentHash = hash
	// StorePath mirrors the content hash so the Mac app's
	// FilePathResolver can locate the blob and render the Preview
	// section. The manual `stash add <file>` path sets both fields;
	// not setting StorePath here meant the detail view's
	// `if let storePath = item.storePath { ImagePreviewSection(…) }`
	// branch never fired and the image was hidden.
	item.StorePath = hash
	item.FileSize = size

	// SourcePath: synthesize a basename with the right extension when
	// the URL doesn't carry one (CDN tokens like
	// lh3.googleusercontent.com/gg/<random>). Mac UI uses the
	// extension as a backup signal for "this is an image" — without
	// it, list-row icons and thumbnail-generator dispatch can fall
	// flat on URLs whose path basename is just a token.
	if ext := extFromMime(mt); ext != "" {
		base := basenameFromURL(srcURL)
		if !strings.Contains(base, ".") {
			base += ext
		}
		item.SourcePath = base
	}

	for _, t := range tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}

	if err := s.CreateItem(context.Background(), item); err != nil {
		return nil, fmt.Errorf("create item: %w", err)
	}
	return item, nil
}

// extFromMime maps the most common CDN MIME types to canonical file
// extensions. Empty for unknown types — caller should leave the
// SourcePath alone in that case rather than guessing.
func extFromMime(mt string) string {
	switch strings.ToLower(strings.TrimSpace(mt)) {
	case "image/jpeg", "image/jpg":  return ".jpg"
	case "image/png":                return ".png"
	case "image/gif":                return ".gif"
	case "image/webp":               return ".webp"
	case "image/svg+xml":            return ".svg"
	case "image/heic":               return ".heic"
	case "image/heif":               return ".heif"
	case "image/bmp":                return ".bmp"
	case "image/tiff":               return ".tiff"
	case "application/pdf":          return ".pdf"
	case "application/zip":          return ".zip"
	case "application/json":         return ".json"
	case "text/plain":               return ".txt"
	case "text/markdown":            return ".md"
	}
	return ""
}

// MARK: - Pick mode

func runPickMode(pageURL string, picks []string, linkSource string, clique, asArchive bool, tags []string, collection string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()
	ctx := context.Background()

	// Resolve link-source: if a URL, look up an item; if not found,
	// auto-create a URL-typed item for the source page. If the user
	// passed an item id, use it directly.
	var linkTargetID string
	if linkSource != "" {
		linkTargetID, err = resolveLinkSource(s, linkSource)
		if err != nil {
			return err
		}
	}

	result := pickResult{Type: "picked"}

	if asArchive {
		// Stage picks to a tempdir, zip them up, stash as one file.
		archiveItem, downloadedTitles, err := stashAsArchive(s, fs, pageURL, picks, tags, collection)
		if err != nil {
			return err
		}
		result.Imported = []pickedItem{{
			ID: archiveItem.ID, URL: archiveItem.URL,
			Title: archiveItem.Title, Type: string(archiveItem.Type),
		}}
		_ = downloadedTitles // available for log/notify in future
	} else {
		for _, pick := range picks {
			body, ctype, finalURL, err := fetchURLBytes(pick, pageURL)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", pick, err))
				continue
			}
			item, err := stashFetchedBytes(s, fs, finalURL, ctype, body, tags, collection, "")
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", pick, err))
				continue
			}
			result.Imported = append(result.Imported, pickedItem{
				ID: item.ID, URL: finalURL, Title: item.Title, Type: string(item.Type),
			})
			if linkTargetID != "" {
				if err := s.LinkItems(ctx, item.ID, linkTargetID, "from-page", false); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("link %s: %v", item.ID, err))
				}
			}
		}
	}
	if linkTargetID != "" {
		result.LinkedTo = linkTargetID
	}

	// Clique-link mode: create N*(N-1)/2 mutual edges between every
	// pair of successfully-imported picks, labeled "clique" so they're
	// distinguishable from the source-spoke "from-page" edges. Skips
	// silently when there's <2 items to link; warns past 15 picks to
	// prevent silent fan-out into very large link tables.
	if clique && len(result.Imported) >= 2 {
		const cliqueSoftCap = 15
		if len(result.Imported) > cliqueSoftCap {
			fmt.Fprintf(os.Stderr,
				"warning: --clique with %d picks creates %d edges; continuing\n",
				len(result.Imported),
				len(result.Imported)*(len(result.Imported)-1)/2,
			)
		}
		edges := 0
		for i := 0; i < len(result.Imported); i++ {
			for j := i + 1; j < len(result.Imported); j++ {
				if err := s.LinkItems(ctx,
					result.Imported[i].ID, result.Imported[j].ID,
					"clique", false,
				); err != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("clique link %s↔%s: %v",
							shortID(result.Imported[i].ID),
							shortID(result.Imported[j].ID), err))
					continue
				}
				edges++
			}
		}
		result.CliqueEdges = edges
	}

	if flagJSON {
		printJSON(result)
		return nil
	}
	for _, p := range result.Imported {
		fmt.Printf("Stashed [%s] %s (%s)\n", shortID(p.ID), p.Title, p.Type)
	}
	if linkTargetID != "" {
		fmt.Printf("Linked all to source item %s\n", shortID(linkTargetID))
	}
	if result.CliqueEdges > 0 {
		fmt.Printf("Cross-linked picks: %d clique edges\n", result.CliqueEdges)
	}
	if len(result.Errors) > 0 {
		fmt.Printf("\n%d errors:\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Println("  " + e)
		}
		return fmt.Errorf("completed with %d errors", len(result.Errors))
	}
	return nil
}

// resolveLinkSource accepts either a URL (looked up by url, created
// if missing) or an item id (assumed to exist). Returns the id of
// the link target item.
func resolveLinkSource(s store.Store, source string) (string, error) {
	ctx := context.Background()
	if isURL(source) {
		if existing, err := s.GetItemByURL(ctx, source); err == nil && existing != nil {
			return existing.ID, nil
		}
		// Create a URL-typed item for the source page.
		item := &model.Item{
			ID:        newFetchID(),
			Type:      model.TypeURL,
			Title:     source,
			URL:       source,
			Metadata:  json.RawMessage("{}"),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.CreateItem(ctx, item); err != nil {
			return "", fmt.Errorf("create source item: %w", err)
		}
		return item.ID, nil
	}
	// Treat as item id; verify it exists.
	if _, err := s.GetItem(ctx, source); err != nil {
		return "", fmt.Errorf("link-source id %s: %w", source, err)
	}
	return source, nil
}


// MARK: - Archive bundling

// stashAsArchive downloads each pick, writes them into a temporary
// zip alongside a small manifest.txt, then stashes the zip itself
// as a single file item. The wrapping is intentionally crude — the
// shipped `archive` package is for stash-portable export/import; an
// asset bundle is just "a zip the user grabbed."
func stashAsArchive(s store.Store, fs *filestore.FileStore, pageURL string, picks []string, tags []string, collection string) (*model.Item, []string, error) {
	tmpDir, err := os.MkdirTemp("", "stash-fetch-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmpDir)

	var titles []string
	for _, pick := range picks {
		body, _, finalURL, err := fetchURLBytes(pick, pageURL)
		if err != nil {
			continue
		}
		name := basenameFromURL(finalURL)
		if name == "" {
			name = "blob"
		}
		// Make filenames unique within the archive.
		dest := filepath.Join(tmpDir, uniqueName(tmpDir, name))
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			continue
		}
		titles = append(titles, name)
	}
	if len(titles) == 0 {
		return nil, nil, fmt.Errorf("no files downloaded successfully")
	}

	// Zip everything in tmpDir.
	zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("stash-fetch-%d.zip", time.Now().Unix()))
	if err := zipDir(tmpDir, zipPath); err != nil {
		return nil, nil, err
	}
	defer os.Remove(zipPath)

	zf, err := os.Open(zipPath)
	if err != nil {
		return nil, nil, err
	}
	defer zf.Close()
	hash, size, err := fs.Save(zf)
	if err != nil {
		return nil, nil, err
	}

	title := fmt.Sprintf("Files from %s", basenameFromURL(pageURL))
	// Archive's URL gets the same redaction treatment as picked
	// items — the page URL is the capture provenance, and for
	// session-only pages it's worth dropping the noise.
	redactedPage, _ := config.RedactURL(pageURL)
	item := &model.Item{
		ID:          newFetchID(),
		Type:        model.TypeFile,
		Title:       title,
		URL:         redactedPage,
		MimeType:    "application/zip",
		ContentHash: hash,
		FileSize:    size,
		Metadata:    json.RawMessage("{}"),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	for _, t := range tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}
	if err := s.CreateItem(context.Background(), item); err != nil {
		return nil, nil, err
	}
	return item, titles, nil
}

// zipDir bundles the regular files inside `srcDir` (non-recursive)
// into a zip at `outPath`. Used by --archive to wrap a batch of
// downloaded picks into a single stashable file. Subdirectories are
// skipped — we only ever stage flat files into srcDir.
func zipDir(srcDir, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		w, err := zw.Create(entry.Name())
		if err != nil {
			return err
		}
		src, err := os.Open(filepath.Join(srcDir, entry.Name()))
		if err != nil {
			return err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
	return name
}

// MARK: - HTTP helper

// fetchURLBytes does one GET with a Safari UA, follows redirects,
// returns the body bytes, the response Content-Type, and the final
// URL after redirects (so callers can compute relative refs against
// the right base). When `referer` is non-empty it's sent so hot-link
// CDN protections don't 403 us.
func fetchURLBytes(rawURL, referer string) ([]byte, string, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 100MB cap so a malicious or accidental gigabyte response
	// doesn't blow the heap. Adjust if real-world workflows need
	// bigger blobs.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, "", "", fmt.Errorf("read body: %w", err)
	}
	final := resp.Request.URL.String()
	return body, resp.Header.Get("Content-Type"), final, nil
}

// printJSONOrText routes an output struct through the existing
// printJSON helper when --json is set, or prints a human-readable
// summary otherwise.
func printJSONOrText(v any) error {
	if flagJSON {
		printJSON(v)
		return nil
	}
	switch t := v.(type) {
	case pageScrape:
		fmt.Printf("%s — %d candidates\n", t.PageURL, len(t.Candidates))
		if t.PageTitle != "" {
			fmt.Println("  " + t.PageTitle)
		}
		for _, c := range t.Candidates {
			label := c.Label
			if label == "" {
				label = c.URL
			}
			fmt.Printf("  [%s] %s\n        %s\n", c.Kind, label, c.URL)
		}
	case directScrape:
		fmt.Printf("Direct file: %s (%s, %s)\n", t.URL, t.MIME, humanSize(t.Size))
	default:
		// Fall back to JSON encode for unhandled shapes.
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
	}
	return nil
}

// MARK: - Helpers

func newFetchID() string {
	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	return ulid.MustNew(ulid.Timestamp(now), entropy).String()
}

// `mime` import is referenced by the URL parser implicitly via the
// extension allowlist; keep the import so the linter doesn't strip
// it if we expand to header-based MIME parsing later.
var _ = mime.TypeByExtension
